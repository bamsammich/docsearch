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
var ErrOutsideRoot = errors.New("path is not inside the configured library root")

// Resolve validates candidate against root and returns the resolved absolute
// path.
//
// The lexical check runs first so a path that is obviously outside the root is
// rejected without the filesystem being touched at all. Only once a candidate
// is lexically acceptable is it resolved through symlinks and checked again --
// a symlink inside the root pointing outside it passes the first check and
// must fail the second.
func Resolve(root, candidate string) (string, error) {
	if root == "" || candidate == "" {
		return "", ErrOutsideRoot
	}
	if !filepath.IsAbs(root) {
		return "", ErrOutsideRoot
	}

	abs := candidate
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)

	if !within(root, abs) {
		return "", ErrOutsideRoot
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Includes "does not exist". Same error either way: the caller must
		// not be able to tell the difference.
		return "", ErrOutsideRoot
	}
	if !within(root, resolved) {
		return "", ErrOutsideRoot
	}
	return resolved, nil
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
