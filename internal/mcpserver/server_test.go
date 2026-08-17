package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bamsammich/docsearch/internal/urlguard"
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

// -- target classification ------------------------------------------------

// A path never carries a scheme, so anything that does was meant as a URL and
// belongs in front of the address guard rather than the path resolver.
func TestATargetWithASchemeIsTreatedAsAURL(t *testing.T) {
	for _, target := range []string{
		"https://docs.example.com/docs",
		"http://docs.example.com",
		"HTTPS://docs.example.com",
		// Refused by the guard, not interpreted as a relative path.
		"file:///etc/passwd",
		"gopher://docs.example.com/x",
	} {
		if !isURL(target) {
			t.Errorf("isURL(%q) = false, want true", target)
		}
	}
}

func TestAPathIsNotMistakenForAURL(t *testing.T) {
	for _, target := range []string{
		"/library/manual.pdf",
		"manual.pdf",
		"sub/dir/manual.pdf",
		"/library/a b/manual.pdf",
		"./manual.pdf",
	} {
		if isURL(target) {
			t.Errorf("isURL(%q) = true, want false", target)
		}
	}
}

// The guard runs at the tool boundary as well as in the worker and on every
// redirect hop. None of the three may assume another ran: a job row is not
// proof that anything validated it, and this is the only one that sees the
// caller.
func TestABlockedURLIsRefusedBeforeAnythingIsQueued(t *testing.T) {
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1/x",
		"http://10.0.0.1/x",
		"https://printer.local/x",
		"file:///etc/passwd",
		"gopher://example.com/x",
	} {
		// Store is nil on purpose: a refusal must happen before anything is
		// enqueued, so reaching the store at all would panic this test.
		d := Deps{LibraryRoots: []string{"/library"}, Log: slog.New(slog.DiscardHandler)}
		_, _, err := d.addDocument(context.Background(), nil, addDocumentInput{Target: target})
		if !errors.Is(err, urlguard.ErrBlocked) {
			t.Errorf("addDocument(%q) error = %v, want ErrBlocked", target, err)
		}
	}
}

// Every rejection looks the same, or the tool reports which internal names
// exist.
func TestURLRejectionsAreIndistinguishable(t *testing.T) {
	d := Deps{LibraryRoots: []string{"/library"}, Log: slog.New(slog.DiscardHandler)}
	var msgs []string
	for _, target := range []string{
		"http://169.254.169.254/x",
		"https://printer.local/x",
		"file:///etc/passwd",
		"https://nonexistent-host-xyz.invalid/x",
	} {
		_, _, err := d.addDocument(context.Background(), nil, addDocumentInput{Target: target})
		if err == nil {
			t.Fatalf("addDocument(%q) unexpectedly succeeded", target)
		}
		msgs = append(msgs, err.Error())
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i] != msgs[0] {
			t.Errorf("rejection messages differ and leak the reason:\n %q\n %q", msgs[0], msgs[i])
		}
	}
}

func TestAnEmptyTargetIsRejected(t *testing.T) {
	d := Deps{LibraryRoots: []string{"/library"}, Log: slog.New(slog.DiscardHandler)}
	if _, _, err := d.addDocument(context.Background(), nil, addDocumentInput{}); err == nil {
		t.Error("empty target should be rejected")
	}
}

// -- server instructions --------------------------------------------------

// The instructions are only worth writing if a client actually receives them,
// and the way to lose them is a one-word regression: New passed nil server
// options for as long as it existed, and nothing about that reads as broken.
// So assert on what a connected client sees rather than on the constant.
func TestAConnectedClientReceivesTheServerInstructions(t *testing.T) {
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()

	server := New(Deps{LibraryRoots: []string{"/library"}, Log: slog.New(slog.DiscardHandler)})
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	if got := cs.InitializeResult().Instructions; got != serverInstructions {
		t.Errorf("client received instructions %q, want the server's", got)
	}
}

// The instructions exist for two habits a session settles before it calls any
// tool: whether it looks here before the web, and what it puts back. A revision
// that drops either one leaves the corpus unread or fills it with write-ups.
func TestTheInstructionsStateBothHabits(t *testing.T) {
	for _, want := range []string{
		"list_documents", // where a session starts, by name
		"the web",        // and that it comes second
		"add_document",   // what to put back
		"never your own", // and what not to
		"asynchronous",   // nothing is searchable when it returns
	} {
		if !strings.Contains(strings.ToLower(serverInstructions), strings.ToLower(want)) {
			t.Errorf("instructions do not mention %q", want)
		}
	}
}

// The description is the only place a caller learns a URL is accepted at all,
// and the only place it learns where a path may live. Both or the tool is
// unusable for one of its two inputs.
func TestTheToolDescriptionNamesBothKindsOfTarget(t *testing.T) {
	got := describeAddDocument([]string{"/library", "/scans"})
	for _, want := range []string{
		"http(s) URL",  // a URL is accepted
		"ONE document", // and what it does with it
		"/library",     // where a path may live
		"/scans",
		"ASYNCHRONOUS", // and that nothing is searchable on return
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description does not mention %q", want)
		}
	}
}
