package libroot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) (root string, inside string) {
	t.Helper()
	base := t.TempDir()
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	inside = filepath.Join(root, "manual.pdf")
	if err := os.WriteFile(inside, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, inside
}

func TestFileInsideRootResolves(t *testing.T) {
	root, inside := setup(t)
	got, err := Resolve(root, inside)
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	if got != inside {
		t.Errorf("Resolve() = %q, want %q", got, inside)
	}
}

func TestNestedFileResolves(t *testing.T) {
	root, _ := setup(t)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(sub, "doc.md")
	if err := os.WriteFile(deep, []byte("# hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, deep); err != nil {
		t.Errorf("Resolve() error = %v, want nil", err)
	}
}

func TestAbsolutePathOutsideRootIsRejected(t *testing.T) {
	root, _ := setup(t)
	if _, err := Resolve(root, "/etc/passwd"); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("error = %v, want ErrOutsideRoot", err)
	}
}

func TestDotDotTraversalIsRejected(t *testing.T) {
	root, _ := setup(t)
	for _, p := range []string{
		filepath.Join(root, "..", "escape.pdf"),
		filepath.Join(root, "a", "..", "..", "escape.pdf"),
		"../../../../etc/passwd",
	} {
		if _, err := Resolve(root, p); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("Resolve(%q) error = %v, want ErrOutsideRoot", p, err)
		}
	}
}

// A symlink inside the root pointing outside it passes the lexical check and
// must be caught by the post-resolution check.
func TestSymlinkInsideRootPointingOutsideIsRejected(t *testing.T) {
	root, _ := setup(t)
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.pdf")
	if err := os.WriteFile(secret, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent.pdf")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Resolve(root, link); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("error = %v, want ErrOutsideRoot for a symlink escaping the root", err)
	}
}

func TestSymlinkedDirectoryEscapeIsRejected(t *testing.T) {
	root, _ := setup(t)
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "x.pdf"), []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "elsewhere")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Resolve(root, filepath.Join(link, "x.pdf")); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("error = %v, want ErrOutsideRoot through a symlinked directory", err)
	}
}

// "/lib" must not be treated as containing "/library".
func TestSiblingDirectoryWithSharedPrefixIsRejected(t *testing.T) {
	base := t.TempDir()
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(resolved, "lib")
	sibling := filepath.Join(resolved, "library")
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(sibling, "doc.pdf")
	if err := os.WriteFile(target, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, target); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("error = %v, want ErrOutsideRoot: %q is not inside %q", err, sibling, root)
	}
}

// Every rejection returns the same error, so the tool cannot be used to probe
// for the existence of files outside the root.
func TestRejectionDoesNotDiscloseExistence(t *testing.T) {
	root, _ := setup(t)
	existsOutside := filepath.Join(t.TempDir(), "real.pdf")
	if err := os.WriteFile(existsOutside, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	absentOutside := "/nonexistent-path-xyz/absent.pdf"
	absentInside := filepath.Join(root, "absent.pdf")

	var msgs []string
	for _, p := range []string{existsOutside, absentOutside, absentInside} {
		_, err := Resolve(root, p)
		if err == nil {
			t.Fatalf("Resolve(%q) unexpectedly succeeded", p)
		}
		msgs = append(msgs, err.Error())
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i] != msgs[0] {
			t.Errorf("error messages differ and leak existence:\n %q\n %q", msgs[0], msgs[i])
		}
	}
}

func TestEmptyInputsAreRejected(t *testing.T) {
	root, _ := setup(t)
	if _, err := Resolve(root, ""); !errors.Is(err, ErrOutsideRoot) {
		t.Error("empty candidate should be rejected")
	}
	if _, err := Resolve("", "/tmp/x"); !errors.Is(err, ErrOutsideRoot) {
		t.Error("empty root should be rejected")
	}
	if _, err := Resolve("relative/root", "relative/root/x"); !errors.Is(err, ErrOutsideRoot) {
		t.Error("relative root should be rejected")
	}
}
