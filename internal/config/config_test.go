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
		Addr:        "127.0.0.1:8765",
		DBPath:      filepath.Join(root, "d.db"),
		LibraryRoot: root,
		BearerToken: "tok",
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
	c.LibraryRoot = link
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if c.LibraryRoot != realResolved {
		t.Errorf("LibraryRoot = %q, want it resolved to %q", c.LibraryRoot, realResolved)
	}
}

func TestNonexistentLibraryRootIsRejected(t *testing.T) {
	c := validCfg(t)
	c.LibraryRoot = filepath.Join(t.TempDir(), "absent")
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for a nonexistent library root")
	}
}
