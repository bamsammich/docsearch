// Package libroot contains the path-validation boundary for the add_document
// tool.
//
// This is a security boundary, not a convenience check. The path arrives in a
// tool call, over a network endpoint, and may have been suggested by the
// content of a document or a web page rather than typed by a person. Two rules
// follow from that:
//
//   - The library root comes from server configuration only. No tool parameter
//     can widen it.
//   - Every rejection returns the same error. Distinguishing "outside the root"
//     from "does not exist" turns the tool into a filesystem probe.
package libroot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideRoot is returned for every rejected path, whatever the reason.
// It deliberately discloses nothing about whether the path exists.
//
// Which directories are configured is not a secret -- add_document names them
// in its description, so a caller can stage a file somewhere acceptable rather
// than guessing. What must stay hidden is anything about the path it asked
// for, which is why one error covers every rejection.
var ErrOutsideRoot = errors.New("path is not inside a configured library root")

// ErrMissing is a path that lies inside a configured root but could not be
// resolved: the file is not there, or the root itself is not mounted. It
// reads exactly as ErrOutsideRoot and wraps it, so a caller learns nothing
// about which files exist. It is separate only so a worker can try again: a
// volume mounts a minute later, while a path outside every root never moves
// inside one.
var ErrMissing error = missing{}

type missing struct{}

func (missing) Error() string { return ErrOutsideRoot.Error() }

func (missing) Unwrap() error { return ErrOutsideRoot }

// Resolve validates candidate against roots and returns the resolved absolute
// path.
//
// A relative candidate is tried against each root in order and the first that
// accepts it wins, so the roots are searched rather than one of them being
// privileged.
//
// Containment is decided once, on two fully resolved paths. Comparing a
// resolved path against an unresolved one refuses files that are plainly
// inside the root, in whichever direction the mismatch runs: a root reached
// through a symlink holds none of its own files, and a caller that resolved
// the path before asking is turned away. Both are ordinary, since /var is a
// symlink to /private/var on macOS.
//
// Resolving the candidate is also what makes a traversal safe to allow this
// far: EvalSymlinks collapses every ".." and every link, so a path dressed up
// to look inside the root arrives as what it really points at and fails the
// containment check on its merits.
func Resolve(roots []string, candidate string) (string, error) {
	if len(roots) == 0 || candidate == "" {
		return "", ErrOutsideRoot
	}
	refusal := ErrOutsideRoot
	for _, configured := range roots {
		if configured == "" || !filepath.IsAbs(configured) {
			continue
		}
		resolved, err := resolveUnder(configured, candidate)
		if err == nil {
			return resolved, nil
		}
		if errors.Is(err, ErrMissing) {
			refusal = ErrMissing
		}
	}
	return "", refusal
}

// resolveUnder resolves candidate against one root. A path the root would
// hold, but that could not be resolved, is ErrMissing; anything else that
// fails is ErrOutsideRoot.
func resolveUnder(configured, candidate string) (string, error) {
	abs := candidate
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(configured, abs)
	}
	lexicallyInside := within(filepath.Clean(configured), filepath.Clean(abs))
	root, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return "", missingIf(lexicallyInside)
	}
	if !filepath.IsAbs(candidate) {
		abs = filepath.Join(root, candidate)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		// Includes "does not exist". The caller sees one refusal either way.
		return "", missingIf(lexicallyInside || within(root, filepath.Clean(abs)))
	}
	if !within(root, resolved) {
		return "", ErrOutsideRoot
	}
	return resolved, nil
}

func missingIf(inside bool) error {
	if inside {
		return ErrMissing
	}
	return ErrOutsideRoot
}

// within reports whether path is root itself or lies beneath it.
//
// Compares path components, never raw string prefixes: "/lib" must not be
// treated as containing "/library".
func within(root, path string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
