package mcpserver

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/urlguard"
	"github.com/bamsammich/docsearch/internal/urlguard/mocks"
)

// ServerSuite covers the tool descriptions, how a target is classified, and
// what add_document refuses at the boundary.
type ServerSuite struct {
	suite.Suite
	resolver *mocks.MockResolver
}

func TestServer(t *testing.T) { suite.Run(t, new(ServerSuite)) }

func (s *ServerSuite) SetupTest() {
	s.resolver = mocks.NewMockResolver(s.T())
}

// deps has no job service on purpose: a refusal must happen before anything
// is queued, so reaching the queue at all would panic the case.
func (s *ServerSuite) deps() Deps {
	return Deps{
		LibraryRoots: []string{"/library"},
		Log:          slog.New(slog.DiscardHandler),
		Resolver:     s.resolver,
	}
}

// add_document rejects every bad path with one error that says nothing about
// the path, so the only way a caller can act on a rejection is to know where
// files are allowed to live. That has to come from the tool description.
func (s *ServerSuite) TestDescribeRootsNamesEveryConfiguredRoot() {
	for _, roots := range [][]string{{"/lib"}, {"/lib", "/downloads", "/scans"}} {
		got := describeRoots(roots)
		for _, root := range roots {
			s.Contains(got, root)
		}
	}
}

// The description has to say what to do about a file that is somewhere else,
// or a caller still has nowhere to go after a rejection.
func (s *ServerSuite) TestDescribeRootsSaysHowToRecover() {
	for _, roots := range [][]string{{"/lib"}, {"/lib", "/downloads"}} {
		s.Contains(describeRoots(roots), "copied", "a file elsewhere must be copied in")
	}
}

// A deployment with accounts takes no paths unless they are shared, and must
// not claim a path would be accepted.
func (s *ServerSuite) TestDescribeRootsWithNoneConfiguredPromisesNothing() {
	got := describeRoots(nil)
	s.Contains(got, "takes no filesystem paths")
	s.Contains(got, "upload_link")
}

// -- target classification ------------------------------------------------

// A path never carries a scheme, so anything that does was meant as a URL and
// belongs in front of the address guard rather than the path resolver.
func (s *ServerSuite) TestATargetWithASchemeIsTreatedAsAURL() {
	for _, target := range []string{
		"https://docs.example.com/docs",
		"http://docs.example.com",
		"HTTPS://docs.example.com",
		// Refused by the guard, not interpreted as a relative path.
		"file:///etc/passwd",
		"gopher://docs.example.com/x",
	} {
		s.True(isURL(target), target)
	}
}

func (s *ServerSuite) TestAPathIsNotMistakenForAURL() {
	for _, target := range []string{
		"/library/manual.pdf",
		"manual.pdf",
		"sub/dir/manual.pdf",
		"/library/a b/manual.pdf",
		"./manual.pdf",
	} {
		s.False(isURL(target), target)
	}
}

// The guard runs at the tool boundary as well as in the worker and on every
// redirect hop. None of the three may assume another ran: a job row is not
// proof that anything validated it, and this is the only one that sees the
// caller.
func (s *ServerSuite) TestABlockedURLIsRefusedBeforeAnythingIsQueued() {
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1/x",
		"http://10.0.0.1/x",
		"https://printer.local/x",
		"file:///etc/passwd",
		"gopher://example.com/x",
	} {
		_, _, err := s.deps().addDocument(s.T().Context(), nil, addDocumentInput{Target: target})
		s.ErrorIs(err, urlguard.ErrBlocked, target)
	}
}

// Every rejection looks the same, or the tool reports which internal names
// exist.
func (s *ServerSuite) TestURLRejectionsAreIndistinguishable() {
	s.resolver.EXPECT().LookupNetIP(mock.Anything, mock.Anything, "absent.example.com").
		Return(nil, errors.New("no such host"))
	var messages []string
	for _, target := range []string{
		"http://169.254.169.254/x",
		"https://printer.local/x",
		"file:///etc/passwd",
		"https://absent.example.com/x",
	} {
		_, _, err := s.deps().addDocument(s.T().Context(), nil, addDocumentInput{Target: target})
		s.Require().Error(err, target)
		messages = append(messages, err.Error())
	}
	for _, m := range messages[1:] {
		s.Equal(messages[0], m, "the messages differ and leak the reason")
	}
}

func (s *ServerSuite) TestAnEmptyTargetIsRejected() {
	_, _, err := s.deps().addDocument(s.T().Context(), nil, addDocumentInput{})
	s.Error(err)
}

// -- server instructions --------------------------------------------------

// The instructions are only worth writing if a client actually receives them,
// and the way to lose them is a one-word regression: New passed nil server
// options for as long as it existed, and nothing about that reads as broken.
// So assert on what a connected client sees rather than on the constant.
func (s *ServerSuite) TestAConnectedClientReceivesTheServerInstructions() {
	ctx := s.T().Context()
	clientT, serverT := mcp.NewInMemoryTransports()

	ss, err := New(s.deps()).Connect(ctx, serverT, nil)
	s.Require().NoError(err)
	defer func() { s.NoError(ss.Close()) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	s.Require().NoError(err)
	defer func() { s.NoError(cs.Close()) }()

	s.Equal(serverInstructions, cs.InitializeResult().Instructions)
}

// The instructions exist for two habits a session settles before it calls any
// tool: whether it looks here before the web, and what it puts back. A revision
// that drops either one leaves the corpus unread or fills it with write-ups.
func (s *ServerSuite) TestTheInstructionsStateBothHabits() {
	lowered := strings.ToLower(serverInstructions)
	for _, want := range []string{
		"list_documents", // where a session starts, by name
		"the web",        // and that it comes second
		"add_document",   // what to put back
		"never your own", // and what not to
		"asynchronous",   // nothing is searchable when it returns
	} {
		s.Contains(lowered, strings.ToLower(want))
	}
}

// The description is the only place a caller learns a URL is accepted at all,
// and the only place it learns where a path may live. Both or the tool is
// unusable for one of its two inputs.
func (s *ServerSuite) TestTheToolDescriptionNamesBothKindsOfTarget() {
	got := describeAddDocument([]string{"/library", "/scans"})
	for _, want := range []string{
		"http(s) URL",  // a URL is accepted
		"ONE document", // and what it does with it
		"/library",     // where a path may live
		"/scans",
		"ASYNCHRONOUS", // and that nothing is searchable on return
	} {
		s.Contains(got, want)
	}
}
