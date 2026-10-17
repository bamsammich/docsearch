package config

import (
	"errors"
	"os"
	"path/filepath"
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
		BearerToken:  "tok",
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
