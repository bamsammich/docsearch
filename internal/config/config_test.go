package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bamsammich/docsearch/internal/owner"
)

func TestIsLoopbackAddr(t *testing.T) {
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
		if got := IsLoopbackAddr(addr); got != want {
			t.Errorf("IsLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func validCfg(t *testing.T) *Config {
	t.Helper()
	root := t.TempDir()
	return &Config{
		Addr:         "127.0.0.1:8765",
		DSN:          "postgres://docsearch@localhost/docsearch",
		LibraryRoots: []string{root},
		BearerToken:  "a-bearer-token-long-enough-to-pass-the-check",
	}
}

func TestRefusesPublicBindWithoutExplicitFlag(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8765", ":8765", "[::]:8765", "192.168.1.5:8765"} {
		c := validCfg(t)
		c.Addr = addr
		if err := c.Validate(); !errors.Is(err, ErrPublicBind) {
			t.Errorf("Validate() with addr %q error = %v, want ErrPublicBind", addr, err)
		}
	}
}

func TestPublicBindAllowedWithExplicitFlag(t *testing.T) {
	c := validCfg(t)
	c.Addr = "0.0.0.0:8765"
	c.AllowPublicBind = true
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil with --allow-public-bind", err)
	}
}

func TestLoopbackBindNeedsNoFlag(t *testing.T) {
	if err := validCfg(t).Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestMissingTokenIsRejected(t *testing.T) {
	c := validCfg(t)
	c.BearerToken = ""
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil, want an error when no bearer token is set")
	}
}

func TestLibraryRootIsResolvedThroughSymlinks(t *testing.T) {
	real := t.TempDir()
	realResolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realResolved, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	c := validCfg(t)
	c.LibraryRoots = []string{link}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(c.LibraryRoots) != 1 || c.LibraryRoots[0] != realResolved {
		t.Errorf("LibraryRoots = %v, want [%q]", c.LibraryRoots, realResolved)
	}
}

func TestNonexistentLibraryRootIsRejected(t *testing.T) {
	c := validCfg(t)
	c.LibraryRoots = []string{filepath.Join(t.TempDir(), "absent")}
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for a nonexistent library root")
	}
}

func TestEveryRootIsResolvedThroughSymlinks(t *testing.T) {
	a, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(b, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	c := validCfg(t)
	c.LibraryRoots = []string{a, link}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if c.LibraryRoots[1] != b {
		t.Errorf("second root = %q, want it resolved to %q", c.LibraryRoots[1], b)
	}
}

// A server that quietly serves three of four configured directories is worse
// than one that will not start: the missing root surfaces much later, as an
// unexplained rejection.
func TestOneUnusableRootFailsStartup(t *testing.T) {
	good, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := validCfg(t)
	c.LibraryRoots = []string{good, filepath.Join(t.TempDir(), "absent")}
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil, want an error when one configured root is unusable")
	}
}

func TestNoRootsIsRejected(t *testing.T) {
	c := validCfg(t)
	c.LibraryRoots = nil
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil, want an error when no library root is configured")
	}
}

// A file passes EvalSymlinks and Abs, and libroot treats a path equal to the
// root as inside it -- so a file named as a root would quietly make exactly
// that file ingestable.
func TestAFileIsNotAValidLibraryRoot(t *testing.T) {
	f := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := validCfg(t)
	c.LibraryRoots = []string{f}
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for a file named as a library root")
	}
}

func TestDuplicateRootsCollapseToOne(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	c := validCfg(t)
	// The same directory three ways: itself, again, and through a symlink.
	c.LibraryRoots = []string{dir, dir, link}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(c.LibraryRoots) != 1 {
		t.Errorf("LibraryRoots = %v, want one entry", c.LibraryRoots)
	}
}

// An unset mode is token, which is what every v1 install already runs: the
// one thing it must not become is the mode that asks for no credential.
func TestAnUnsetAuthModeIsToken(t *testing.T) {
	c := validCfg(t)
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if c.Auth != owner.Token {
		t.Errorf("Auth = %v, want token", c.Auth)
	}
}

func TestTokenModeNeedsAToken(t *testing.T) {
	c := validCfg(t)
	c.Auth = owner.Token
	c.BearerToken = ""
	if err := c.Validate(); err == nil {
		t.Error("token mode with no token should be refused")
	}
}

// Mode none asks for no credential, so it needs none configured.
func TestNoneModeNeedsNoToken(t *testing.T) {
	c := validCfg(t)
	c.Auth = owner.None
	c.BearerToken = ""
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

// --allow-public-bind opts into being reachable, not into serving a library
// to whoever arrives. Mode none on a public address is an open document
// store, and no flag says that clearly enough to act on.
func TestNoneModeIsRefusedOffLoopbackEvenWithTheFlag(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8765", ":8765", "192.168.1.5:8765"} {
		c := validCfg(t)
		c.Auth = owner.None
		c.Addr = addr
		c.AllowPublicBind = true
		if err := c.Validate(); !errors.Is(err, ErrOpenToTheNetwork) {
			t.Errorf("Validate() with none on %q error = %v, want ErrOpenToTheNetwork", addr, err)
		}
	}
}

func TestNoneModeIsAllowedOnLoopback(t *testing.T) {
	c := validCfg(t)
	c.Auth = owner.None
	c.Addr = "127.0.0.1:8765"
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

// oidcCfg is an oidc deployment with every setting it cannot run without.
func oidcCfg(t *testing.T) *Config {
	t.Helper()
	c := validCfg(t)
	c.Auth = owner.OIDC
	c.BearerToken = ""
	c.OIDC = OIDC{Issuer: "https://issuer.example", Resource: "https://docs.example.com/mcp"}
	c.LibraryRoots = nil
	return c
}

// Mode oidc reads tokens from a provider, so it needs no static token.
func TestOIDCModeNeedsNoToken(t *testing.T) {
	if err := oidcCfg(t).Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

// Each of these could only fail on the first request, so each fails at
// startup instead.
func TestAnOIDCConfigurationThatCannotWorkIsRefused(t *testing.T) {
	cases := map[string]func(*OIDC){
		"no issuer":                    func(o *OIDC) { o.Issuer = "" },
		"no resource":                  func(o *OIDC) { o.Resource = "" },
		"a client ID without a secret": func(o *OIDC) { o.ClientID = "docsearch" },
		"a secret without a client ID": func(o *OIDC) { o.ClientSecret = "secret" },
		"a claim name with no value":   func(o *OIDC) { o.RequiredClaimName = "groups" },
		"a claim value with no name":   func(o *OIDC) { o.RequiredClaimValue = "docsearch-users" },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			c := oidcCfg(t)
			spoil(&c.OIDC)
			if err := c.Validate(); err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
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
func TestAWeakOrPlaceholderTokenIsRefused(t *testing.T) {
	for name, token := range map[string]string{
		"short":       "tok",
		"placeholder": "CHANGE-ME-generate-a-real-token-before-applying",
	} {
		c := validCfg(t)
		c.BearerToken = token
		if err := c.Validate(); err == nil {
			t.Errorf("%s token: Validate() = nil, want a refusal", name)
		}
	}
}

// A deployment with accounts takes no paths unless the operator says every
// account may read the same roots.
func TestAccountsTakeNoPathsUnlessShared(t *testing.T) {
	for _, mode := range []owner.Mode{owner.OIDC, owner.Password} {
		c := oidcCfg(t)
		c.Auth = mode
		if err := c.Validate(); err != nil {
			t.Errorf("%v with no roots: Validate() error = %v, want nil", mode, err)
		}

		c = oidcCfg(t)
		c.Auth = mode
		c.LibraryRoots = []string{t.TempDir()}
		if err := c.Validate(); !errors.Is(err, ErrSharedPaths) {
			t.Errorf("%v with roots: Validate() error = %v, want ErrSharedPaths", mode, err)
		}

		c.SharedPaths = true
		if err := c.Validate(); err != nil {
			t.Errorf("%v with shared roots: Validate() error = %v, want nil", mode, err)
		}
	}
}
