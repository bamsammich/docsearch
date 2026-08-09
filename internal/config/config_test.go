package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
		DBPath:       filepath.Join(root, "d.db"),
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

func TestRootsParseFromOnePathList(t *testing.T) {
	sep := string(filepath.ListSeparator)
	for _, tc := range []struct {
		name string
		in   string
		want int
	}{
		{"single path stays one root", "/a", 1},
		{"two paths", "/a" + sep + "/b", 2},
		{"blank entries are dropped", "/a" + sep + sep + " " + sep + "/b", 2},
		{"empty is no roots", "", 0},
	} {
		if got := len(SplitRoots(tc.in)); got != tc.want {
			t.Errorf("%s: SplitRoots(%q) = %d roots, want %d", tc.name, tc.in, got, tc.want)
		}
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
