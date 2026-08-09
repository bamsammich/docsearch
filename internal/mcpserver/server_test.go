package mcpserver

import (
	"strings"
	"testing"
)

// add_document rejects every bad path with one error that says nothing about
// the path, so the only way a caller can act on a rejection is to know where
// files are allowed to live. That has to come from the tool description.
func TestDescribeRootsNamesEveryConfiguredRoot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roots []string
		want  []string
	}{
		{"one root", []string{"/lib"}, []string{"/lib"}},
		{"several roots", []string{"/lib", "/downloads", "/scans"},
			[]string{"/lib", "/downloads", "/scans"}},
	} {
		got := describeRoots(tc.roots)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: description %q does not name root %q", tc.name, got, want)
			}
		}
	}
}

// The description has to say what to do about a file that is somewhere else,
// or a caller still has nowhere to go after a rejection.
func TestDescribeRootsSaysHowToRecover(t *testing.T) {
	for _, roots := range [][]string{{"/lib"}, {"/lib", "/downloads"}} {
		got := describeRoots(roots)
		if !strings.Contains(got, "copied") {
			t.Errorf("description for %v does not say a file elsewhere must be copied in: %q",
				roots, got)
		}
	}
}

// Validate refuses to start with no roots, so this branch should be
// unreachable; it must still not claim a path would be accepted.
func TestDescribeRootsWithNoneConfiguredPromisesNothing(t *testing.T) {
	got := describeRoots(nil)
	if !strings.Contains(got, "cannot accept any path") {
		t.Errorf("description with no roots = %q, want it to say no path is acceptable", got)
	}
}
