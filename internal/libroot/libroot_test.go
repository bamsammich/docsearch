package libroot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

// RootSuite covers which paths a set of roots holds, and that every refusal
// reads the same.
type RootSuite struct{ suite.Suite }

func TestRoots(t *testing.T) { suite.Run(t, new(RootSuite)) }

// root is a fresh root, resolved, holding manual.pdf.
func (s *RootSuite) root() (string, string) {
	root := s.resolvedDir()
	inside := filepath.Join(root, "manual.pdf")
	s.write(inside)
	return root, inside
}

// resolvedDir is a fresh directory, as a resolved root would name it.
func (s *RootSuite) resolvedDir() string {
	dir, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	return dir
}

func (s *RootSuite) write(path string) {
	s.Require().NoError(os.WriteFile(path, []byte("%PDF-1.4"), 0o600))
}

func (s *RootSuite) mkdir(path string) {
	s.Require().NoError(os.MkdirAll(path, 0o750))
}

// link makes a symlink, skipping the case where none can be made.
func (s *RootSuite) link(target, link string) {
	if err := os.Symlink(target, link); err != nil {
		s.T().Skipf("symlinks unavailable: %v", err)
	}
}

// refused checks a refusal is the uniform one, and whether it is the kind a
// worker retries.
func (s *RootSuite) refused(err error, missing bool, context ...any) {
	s.Require().ErrorIs(err, ErrOutsideRoot, context...)
	s.Equal(missing, errors.Is(err, ErrMissing), context...)
	s.Equal(ErrOutsideRoot.Error(), err.Error(), "the message is the uniform refusal")
}

// sameRefusal checks every candidate is refused with one message, so the
// refusal cannot be used to probe for files outside the roots.
func (s *RootSuite) sameRefusal(roots []string, candidates ...string) {
	var messages []string
	for _, p := range candidates {
		_, err := Resolve(roots, p)
		s.Require().Error(err, p)
		messages = append(messages, err.Error())
	}
	for _, m := range messages[1:] {
		s.Equal(messages[0], m, "the messages differ and leak existence")
	}
}

func (s *RootSuite) TestFileInsideRootResolves() {
	root, inside := s.root()
	got, err := Resolve([]string{root}, inside)
	s.Require().NoError(err)
	s.Equal(inside, got)
}

func (s *RootSuite) TestNestedFileResolves() {
	root, _ := s.root()
	sub := filepath.Join(root, "a", "b")
	s.mkdir(sub)
	deep := filepath.Join(sub, "doc.md")
	s.write(deep)
	_, err := Resolve([]string{root}, deep)
	s.NoError(err)
}

func (s *RootSuite) TestAbsolutePathOutsideRootIsRejected() {
	root, _ := s.root()
	_, err := Resolve([]string{root}, "/etc/passwd")
	s.ErrorIs(err, ErrOutsideRoot)
}

func (s *RootSuite) TestDotDotTraversalIsRejected() {
	root, _ := s.root()
	for _, p := range []string{
		filepath.Join(root, "..", "escape.pdf"),
		filepath.Join(root, "a", "..", "..", "escape.pdf"),
		"../../../../etc/passwd",
	} {
		_, err := Resolve([]string{root}, p)
		s.ErrorIs(err, ErrOutsideRoot, p)
	}
}

// A symlink inside the root pointing outside it passes the lexical check and
// must be caught by the post-resolution check.
func (s *RootSuite) TestSymlinkInsideRootPointingOutsideIsRejected() {
	root, _ := s.root()
	secret := filepath.Join(s.T().TempDir(), "secret.pdf")
	s.write(secret)
	link := filepath.Join(root, "innocent.pdf")
	s.link(secret, link)
	_, err := Resolve([]string{root}, link)
	s.ErrorIs(err, ErrOutsideRoot)
}

func (s *RootSuite) TestSymlinkedDirectoryEscapeIsRejected() {
	root, _ := s.root()
	outside := s.T().TempDir()
	s.write(filepath.Join(outside, "x.pdf"))
	link := filepath.Join(root, "elsewhere")
	s.link(outside, link)
	_, err := Resolve([]string{root}, filepath.Join(link, "x.pdf"))
	s.ErrorIs(err, ErrOutsideRoot)
}

// "/lib" must not be treated as containing "/library".
func (s *RootSuite) TestSiblingDirectoryWithSharedPrefixIsRejected() {
	base := s.resolvedDir()
	root := filepath.Join(base, "lib")
	sibling := filepath.Join(base, "library")
	s.mkdir(root)
	s.mkdir(sibling)
	target := filepath.Join(sibling, "doc.pdf")
	s.write(target)
	_, err := Resolve([]string{root}, target)
	s.ErrorIs(err, ErrOutsideRoot)
}

// Every rejection returns the same error, so the tool cannot be used to probe
// for the existence of files outside the root.
func (s *RootSuite) TestRejectionDoesNotDiscloseExistence() {
	root, _ := s.root()
	existsOutside := filepath.Join(s.T().TempDir(), "real.pdf")
	s.write(existsOutside)
	s.sameRefusal([]string{root},
		existsOutside, "/nonexistent-path-xyz/absent.pdf", filepath.Join(root, "absent.pdf"))
}

func (s *RootSuite) TestEmptyInputsAreRejected() {
	root, _ := s.root()
	_, err := Resolve([]string{root}, "")
	s.Require().ErrorIs(err, ErrOutsideRoot, "an empty candidate")
	_, err = Resolve(nil, "/tmp/x")
	s.Require().ErrorIs(err, ErrOutsideRoot, "no root")
	_, err = Resolve([]string{"relative/root"}, "relative/root/x")
	s.ErrorIs(err, ErrOutsideRoot, "a relative root")
}

// -- multiple roots -------------------------------------------------------

func (s *RootSuite) TestAFileInAnyConfiguredRootResolves() {
	a, b := s.resolvedDir(), s.resolvedDir()
	for _, root := range []string{a, b} {
		target := filepath.Join(root, "doc.pdf")
		s.write(target)
		got, err := Resolve([]string{a, b}, target)
		s.Require().NoError(err, root)
		s.Equal(target, got)
	}
}

func (s *RootSuite) TestAFileInNoConfiguredRootIsRejected() {
	a, b := s.resolvedDir(), s.resolvedDir()
	outside := filepath.Join(s.T().TempDir(), "doc.pdf")
	s.write(outside)
	_, err := Resolve([]string{a, b}, outside)
	s.ErrorIs(err, ErrOutsideRoot)
}

// An unusable entry must not stop a later root from accepting a path, and must
// not accept anything itself.
func (s *RootSuite) TestAnUnusableRootIsSkippedNotFatal() {
	a := s.resolvedDir()
	target := filepath.Join(a, "doc.pdf")
	s.write(target)
	roots := []string{"", "relative/root", a}
	_, err := Resolve(roots, target)
	s.Require().NoError(err, "a usable root follows the unusable ones")
	_, err = Resolve(roots, "/etc/passwd")
	s.ErrorIs(err, ErrOutsideRoot, "an unusable root must not widen what is accepted")
}

// A traversal that escapes one root must not be accepted because it lands in
// another: each root is checked on its own terms.
func (s *RootSuite) TestTraversalBetweenRootsIsStillRejected() {
	a, b := s.resolvedDir(), s.resolvedDir()
	target := filepath.Join(b, "doc.pdf")
	s.write(target)
	// Reaches b's file, but written as an escape from a.
	rel, err := filepath.Rel(a, target)
	s.Require().NoError(err)
	_, err = Resolve([]string{a}, filepath.Join(a, rel))
	s.ErrorIs(err, ErrOutsideRoot)
}

// Multiple roots must not make rejections distinguishable.
func (s *RootSuite) TestMultiRootRejectionDoesNotDiscloseExistence() {
	a, b := s.resolvedDir(), s.resolvedDir()
	existsOutside := filepath.Join(s.T().TempDir(), "real.pdf")
	s.write(existsOutside)
	s.sameRefusal([]string{a, b},
		existsOutside, "/nonexistent-path-xyz/absent.pdf",
		filepath.Join(a, "absent.pdf"), filepath.Join(b, "absent.pdf"))
}

// A root reached through a symlink is the ordinary case on macOS, where
// every temporary directory sits under /var, itself a symlink to
// /private/var. Every test above resolves the root before passing it, which
// is why none of them caught a root configured as an operator would write it.
func (s *RootSuite) TestARootReachedThroughASymlinkContainsItsOwnFiles() {
	base := s.T().TempDir()
	library := filepath.Join(base, "library")
	s.mkdir(library)
	link := filepath.Join(base, "configured")
	s.link(library, link)
	inside := filepath.Join(link, "manual.pdf")
	s.write(inside)

	got, err := Resolve([]string{link}, inside)
	s.Require().NoError(err)
	want, err := filepath.EvalSymlinks(inside)
	s.Require().NoError(err)
	s.Equal(want, got)
}

// The protection the second check exists for, with a symlinked root: a link
// inside the root pointing out of it still resolves outside and is refused.
func (s *RootSuite) TestASymlinkOutOfASymlinkedRootIsStillRejected() {
	base := s.T().TempDir()
	library := filepath.Join(base, "library")
	outside := filepath.Join(base, "elsewhere")
	s.mkdir(library)
	s.mkdir(outside)
	secret := filepath.Join(outside, "secret.pdf")
	s.write(secret)
	link := filepath.Join(base, "configured")
	s.link(library, link)
	s.link(secret, filepath.Join(library, "escape.pdf"))

	_, err := Resolve([]string{link}, filepath.Join(link, "escape.pdf"))
	s.ErrorIs(err, ErrOutsideRoot)
}

// A caller that resolved the path before asking is the other half of the
// symlink problem: the full-stack suite resolves every path it writes, and
// comparing that against a root spelled as the operator wrote it refused
// every fixture in the library.
func (s *RootSuite) TestAResolvedCandidateIsAcceptedUnderAnUnresolvedRoot() {
	base := s.T().TempDir()
	library := filepath.Join(base, "library")
	s.mkdir(library)
	link := filepath.Join(base, "configured")
	s.link(library, link)
	inside := filepath.Join(library, "manual.pdf")
	s.write(inside)
	resolved, err := filepath.EvalSymlinks(inside)
	s.Require().NoError(err)

	// The root as configured, the candidate already resolved.
	got, err := Resolve([]string{link}, resolved)
	s.Require().NoError(err)
	s.Equal(resolved, got)
}

// A path a root would hold but that is not there right now, or whose root is
// not mounted, is ErrMissing so a worker can retry it. It reads exactly as a
// path outside every root, so a caller learns nothing about which files
// exist.
func (s *RootSuite) TestAMissingPathInsideARootIsDistinguishedOnlyForTheWorker() {
	root := s.T().TempDir()
	unmounted := filepath.Join(root, "unmounted")
	cases := map[string]refusalCase{
		"a missing file inside a root": {
			candidate: filepath.Join(root, "gone.pdf"), roots: []string{root}, missing: true,
		},
		"a root that is not mounted": {
			candidate: filepath.Join(unmounted, "a.pdf"), roots: []string{unmounted}, missing: true,
		},
		"a path outside every root": {candidate: "/etc/passwd", roots: []string{root}},
		"a traversal out of the root": {
			candidate: filepath.Join(root, "..", "x.pdf"), roots: []string{root},
		},
	}
	for name, c := range cases {
		_, err := Resolve(c.roots, c.candidate)
		s.refused(err, c.missing, name)
	}
}

// refusalCase is a candidate a set of roots refuses, and whether a worker
// should retry it.
type refusalCase struct {
	candidate string
	roots     []string
	missing   bool
}
