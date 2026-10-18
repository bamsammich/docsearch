package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/owner"
)

// ConfigSuite covers what Validate accepts, refuses and resolves.
type ConfigSuite struct{ suite.Suite }

func TestConfig(t *testing.T) { suite.Run(t, new(ConfigSuite)) }

func (s *ConfigSuite) TestIsLoopbackAddr() {
	cases := map[string]bool{
		"127.0.0.1:8765": true,
		"localhost:8765": true,
		"[::1]:8765":     true,
		"127.0.0.53:80":  true,
		"0.0.0.0:8765":   false,
		"[::]:8765":      false,
		":8765":          false,
		"100.64.0.1:80":  false, // a Tailscale address is still not loopback
		"192.168.1.5:80": false,
	}
	for addr, want := range cases {
		s.Equal(want, IsLoopbackAddr(addr), addr)
	}
}

// valid is a configuration Validate accepts, with a root of its own.
func (s *ConfigSuite) valid() *Config {
	return &Config{
		Addr:         "127.0.0.1:8765",
		DSN:          "postgres://docsearch@localhost/docsearch",
		LibraryRoots: []string{s.T().TempDir()},
		BearerToken:  "a-bearer-token-long-enough-to-pass-the-check",
	}
}

// oidc is an oidc deployment with every setting it cannot run without.
func (s *ConfigSuite) oidc() *Config {
	c := s.valid()
	c.Auth = owner.OIDC
	c.BearerToken = ""
	c.OIDC = OIDC{Issuer: "https://issuer.example", Resource: "https://docs.example.com/mcp"}
	c.LibraryRoots = nil
	return c
}

// resolved is a fresh directory, as Validate will resolve it.
func (s *ConfigSuite) resolved() string {
	dir, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	return dir
}

// linkTo is a symlink to target, skipping the case where none can be made.
func (s *ConfigSuite) linkTo(target string) string {
	link := filepath.Join(s.T().TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		s.T().Skipf("symlinks unavailable: %v", err)
	}
	return link
}

func (s *ConfigSuite) TestRefusesPublicBindWithoutExplicitFlag() {
	for _, addr := range []string{"0.0.0.0:8765", ":8765", "[::]:8765", "192.168.1.5:8765"} {
		c := s.valid()
		c.Addr = addr
		s.ErrorIs(c.Validate(), ErrPublicBind, addr)
	}
}

func (s *ConfigSuite) TestPublicBindAllowedWithExplicitFlag() {
	c := s.valid()
	c.Addr = "0.0.0.0:8765"
	c.AllowPublicBind = true
	s.NoError(c.Validate())
}

func (s *ConfigSuite) TestLoopbackBindNeedsNoFlag() {
	s.NoError(s.valid().Validate())
}

func (s *ConfigSuite) TestMissingTokenIsRejected() {
	c := s.valid()
	c.BearerToken = ""
	s.Error(c.Validate())
}

func (s *ConfigSuite) TestLibraryRootIsResolvedThroughSymlinks() {
	target := s.resolved()
	c := s.valid()
	c.LibraryRoots = []string{s.linkTo(target)}
	s.Require().NoError(c.Validate())
	s.Equal([]string{target}, c.LibraryRoots)
}

func (s *ConfigSuite) TestNonexistentLibraryRootIsRejected() {
	c := s.valid()
	c.LibraryRoots = []string{filepath.Join(s.T().TempDir(), "absent")}
	s.Error(c.Validate())
}

func (s *ConfigSuite) TestEveryRootIsResolvedThroughSymlinks() {
	a, b := s.resolved(), s.resolved()
	c := s.valid()
	c.LibraryRoots = []string{a, s.linkTo(b)}
	s.Require().NoError(c.Validate())
	s.Equal(b, c.LibraryRoots[1], "the second root is resolved too")
}

// A server that quietly serves three of four configured directories is worse
// than one that will not start: the missing root surfaces much later, as an
// unexplained rejection.
func (s *ConfigSuite) TestOneUnusableRootFailsStartup() {
	c := s.valid()
	c.LibraryRoots = []string{s.resolved(), filepath.Join(s.T().TempDir(), "absent")}
	s.Error(c.Validate())
}

func (s *ConfigSuite) TestNoRootsIsRejected() {
	c := s.valid()
	c.LibraryRoots = nil
	s.Error(c.Validate())
}

// A file passes EvalSymlinks and Abs, and libroot treats a path equal to the
// root as inside it -- so a file named as a root would quietly make exactly
// that file ingestable.
func (s *ConfigSuite) TestAFileIsNotAValidLibraryRoot() {
	f := filepath.Join(s.T().TempDir(), "notes.txt")
	s.Require().NoError(os.WriteFile(f, []byte("x"), 0o600))
	c := s.valid()
	c.LibraryRoots = []string{f}
	s.Error(c.Validate())
}

func (s *ConfigSuite) TestDuplicateRootsCollapseToOne() {
	dir := s.resolved()
	c := s.valid()
	// The same directory three ways: itself, again, and through a symlink.
	c.LibraryRoots = []string{dir, dir, s.linkTo(dir)}
	s.Require().NoError(c.Validate())
	s.Len(c.LibraryRoots, 1)
}

// An unset mode is token, which is what every v1 install already runs: the
// one thing it must not become is the mode that asks for no credential.
func (s *ConfigSuite) TestAnUnsetAuthModeIsToken() {
	c := s.valid()
	s.Require().NoError(c.Validate())
	s.Equal(owner.Token, c.Auth)
}

func (s *ConfigSuite) TestTokenModeNeedsAToken() {
	c := s.valid()
	c.Auth = owner.Token
	c.BearerToken = ""
	s.Error(c.Validate())
}

// Mode none asks for no credential, so it needs none configured.
func (s *ConfigSuite) TestNoneModeNeedsNoToken() {
	c := s.valid()
	c.Auth = owner.None
	c.BearerToken = ""
	s.NoError(c.Validate())
}

// --allow-public-bind opts into being reachable, not into serving a library
// to whoever arrives. Mode none on a public address is an open document
// store, and no flag says that clearly enough to act on.
func (s *ConfigSuite) TestNoneModeIsRefusedOffLoopbackEvenWithTheFlag() {
	for _, addr := range []string{"0.0.0.0:8765", ":8765", "192.168.1.5:8765"} {
		c := s.valid()
		c.Auth = owner.None
		c.Addr = addr
		c.AllowPublicBind = true
		s.ErrorIs(c.Validate(), ErrOpenToTheNetwork, addr)
	}
}

func (s *ConfigSuite) TestNoneModeIsAllowedOnLoopback() {
	c := s.valid()
	c.Auth = owner.None
	c.Addr = "127.0.0.1:8765"
	s.NoError(c.Validate())
}

// Mode oidc reads tokens from a provider, so it needs no static token.
func (s *ConfigSuite) TestOIDCModeNeedsNoToken() {
	s.NoError(s.oidc().Validate())
}

// Each of these could only fail on the first request, so each fails at
// startup instead.
func (s *ConfigSuite) TestAnOIDCConfigurationThatCannotWorkIsRefused() {
	cases := map[string]func(*OIDC){
		"no issuer":                    func(o *OIDC) { o.Issuer = "" },
		"no resource":                  func(o *OIDC) { o.Resource = "" },
		"a client ID without a secret": func(o *OIDC) { o.ClientID = "docsearch" },
		"a secret without a client ID": func(o *OIDC) { o.ClientSecret = "secret" },
		"a claim name with no value":   func(o *OIDC) { o.RequiredClaimName = "groups" },
		"a claim value with no name":   func(o *OIDC) { o.RequiredClaimValue = "docsearch-users" },
	}
	for name, spoil := range cases {
		c := s.oidc()
		spoil(&c.OIDC)
		s.Error(c.Validate(), name)
	}
}

func TestBrowserOriginsAddTheServersOwnOrigin(t *testing.T) {
	cfg := Config{
		AllowedOrigins: []string{"https://claude.ai"},
		PublicURL:      "https://docs.example.org/base",
	}
	got := cfg.BrowserOrigins()
	want := []string{"https://claude.ai", "https://docs.example.org"}
	if !slices.Equal(got, want) {
		t.Errorf("BrowserOrigins() = %v, want %v", got, want)
	}
}

func TestBrowserOriginsDoNotRepeatAListedOrigin(t *testing.T) {
	cfg := Config{
		AllowedOrigins: []string{"HTTPS://Docs.Example.org"},
		PublicURL:      "https://docs.example.org",
	}
	if got := cfg.BrowserOrigins(); len(got) != 1 {
		t.Errorf("BrowserOrigins() = %v, want the one listed origin", got)
	}
}

func TestBrowserOriginsLeaveTheAllowlistUntouched(t *testing.T) {
	cfg := Config{AllowedOrigins: []string{"https://claude.ai"}, PublicURL: "http://127.0.0.1:8765"}
	_ = cfg.BrowserOrigins()
	if len(cfg.AllowedOrigins) != 1 {
		t.Errorf("AllowedOrigins grew to %v", cfg.AllowedOrigins)
	}
}

func TestBrowserOriginsAcceptBothLoopbackSpellingsOnALoopbackListener(t *testing.T) {
	cfg := Config{PublicURL: "http://docs.tailnet.example:18765", Addr: "127.0.0.1:18765"}
	got := cfg.BrowserOrigins()
	want := []string{
		"http://docs.tailnet.example:18765",
		"http://127.0.0.1:18765",
		"http://localhost:18765",
	}
	if !slices.Equal(got, want) {
		t.Errorf("BrowserOrigins() = %v, want %v", got, want)
	}
}

func TestBrowserOriginsAddNoLoopbackOnAPublicListener(t *testing.T) {
	cfg := Config{PublicURL: "https://docs.example.org", Addr: "0.0.0.0:8765"}
	if got := cfg.BrowserOrigins(); len(got) != 1 {
		t.Errorf("BrowserOrigins() = %v, want only the public origin", got)
	}
}

// A bearer token is the whole of mode token's security, so one that is
// short or still the example's placeholder is refused at startup.
func (s *ConfigSuite) TestAWeakOrPlaceholderTokenIsRefused() {
	for name, token := range map[string]string{
		"short":       "tok",
		"placeholder": "CHANGE-ME-generate-a-real-token-before-applying",
	} {
		c := s.valid()
		c.BearerToken = token
		s.Error(c.Validate(), name)
	}
}

// A deployment with accounts takes no paths unless the operator says every
// account may read the same roots.
func (s *ConfigSuite) TestAccountsTakeNoPathsUnlessShared() {
	for _, mode := range []owner.Mode{owner.OIDC, owner.Password} {
		c := s.oidc()
		c.Auth = mode
		s.Require().NoError(c.Validate(), "%v with no roots", mode)

		c = s.oidc()
		c.Auth = mode
		c.LibraryRoots = []string{s.T().TempDir()}
		s.Require().ErrorIs(c.Validate(), ErrSharedPaths, "%v with roots", mode)

		c.SharedPaths = true
		s.NoError(c.Validate(), "%v with shared roots", mode)
	}
}
