package connectapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/api/connectapi"
	"github.com/bamsammich/docsearch/internal/api/connectapi/mocks"
	documentv1 "github.com/bamsammich/docsearch/internal/api/docsearch/document/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/document/v1/documentv1connect"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/service/document"
)

// noCopies is the copier a suite that copies nothing is built with.
var noCopies connectapi.CopiesFor = func(string) (connectapi.Copies, error) { return nil, nil }

// CopyAPISuite drives Copy and Move over HTTP. The suite's user owns their
// own library, edits platform and reads runbooks.
type CopyAPISuite struct {
	suite.Suite
	copies *mocks.MockCopies
	client documentv1connect.DocumentServiceClient
}

func TestCopyAPI(t *testing.T) { suite.Run(t, new(CopyAPISuite)) }

func (s *CopyAPISuite) SetupTest() {
	s.copies = mocks.NewMockCopies(s.T())
	path, handler := connectapi.NewDocumentServer(
		func(pgsession.Scope) (connectapi.Documents, error) { return nil, nil },
		shelves, errNotFound,
		func(string) (connectapi.Copies, error) { return s.copies, nil },
	)
	mux := http.NewServeMux()
	mux.Handle(path, owner.Middleware(owner.Builtin, handler))
	server := httptest.NewServer(mux)
	s.T().Cleanup(server.Close)
	s.client = documentv1connect.NewDocumentServiceClient(server.Client(), server.URL)
}

// A reader of runbooks copies one of its documents into a library they
// edit, and the copy keeps its doc_id.
func (s *CopyAPISuite) TestAReaderCopiesOut() {
	s.copies.EXPECT().Start(mock.Anything, document.CopyRequest{
		From: runbooksID, DocID: "pager", To: platformID,
	}).Return(42, nil)

	res, err := s.client.Copy(s.T().Context(), connect.NewRequest(&documentv1.CopyRequest{
		DocId: "runbooks/pager", Library: "platform",
	}))
	s.Require().NoError(err)
	s.Equal(int64(42), res.Msg.GetJobId())
	s.Equal("platform/pager", res.Msg.GetDocId())
}

// A copy into your own library comes back bare, as every identifier there
// does, under the name --as gave it.
func (s *CopyAPISuite) TestACopyIntoYourOwnLibraryTakesTheNameAsGives() {
	s.copies.EXPECT().Start(mock.Anything, document.CopyRequest{
		From: platformID, DocID: "pager", To: owner.Builtin, As: "pager-2",
	}).Return(7, nil)

	res, err := s.client.Copy(s.T().Context(), connect.NewRequest(&documentv1.CopyRequest{
		DocId: "platform/pager", Library: "personal", As: "pager-2",
	}))
	s.Require().NoError(err)
	s.Equal("pager-2", res.Msg.GetDocId())
}

func (s *CopyAPISuite) TestAReaderCannotMoveOut() {
	_, err := s.client.Move(s.T().Context(), connect.NewRequest(&documentv1.MoveRequest{
		DocId: "runbooks/pager", Library: "platform",
	}))
	s.Equal(connect.CodePermissionDenied, connect.CodeOf(err))
}

func (s *CopyAPISuite) TestNothingGoesIntoALibraryYouOnlyRead() {
	_, err := s.client.Copy(s.T().Context(), connect.NewRequest(&documentv1.CopyRequest{
		DocId: "platform/pager", Library: "runbooks",
	}))
	s.Equal(connect.CodePermissionDenied, connect.CodeOf(err))
}

func (s *CopyAPISuite) TestAnEditorMovesBetweenLibrariesTheyEdit() {
	s.copies.EXPECT().Start(mock.Anything, document.CopyRequest{
		From: platformID, DocID: "pager", To: owner.Builtin, Move: true,
	}).Return(9, nil)

	res, err := s.client.Move(s.T().Context(), connect.NewRequest(&documentv1.MoveRequest{
		DocId: "platform/pager", Library: "personal",
	}))
	s.Require().NoError(err)
	s.Equal(int64(9), res.Msg.GetJobId())
}

// A library the user reads under no label is NotFound, as everywhere else.
func (s *CopyAPISuite) TestAnUnknownLibraryIsNotFound() {
	_, err := s.client.Copy(s.T().Context(), connect.NewRequest(&documentv1.CopyRequest{
		DocId: "platform/pager", Library: "secret",
	}))
	s.Equal(connect.CodeNotFound, connect.CodeOf(err))
}

func (s *CopyAPISuite) TestBadRequestsAreRefusedBeforeAnythingStarts() {
	for name, req := range map[string]*documentv1.CopyRequest{
		"no document":  {Library: "platform"},
		"no library":   {DocId: "pager"},
		"same library": {DocId: "platform/pager", Library: "platform"},
		"bad --as":     {DocId: "pager", Library: "platform", As: "Not/OK"},
	} {
		_, err := s.client.Copy(s.T().Context(), connect.NewRequest(req))
		s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err), name)
	}
}

func (s *CopyAPISuite) TestEachRefusalAnswersWithItsCode() {
	for refusal, want := range map[error]connect.Code{
		document.Taken{DocID: "pager"}:       connect.CodeAlreadyExists,
		document.AlreadyHeld{DocID: "pager"}: connect.CodeAlreadyExists,
		document.SourceHeld{DocID: "pager"}:  connect.CodeAlreadyExists,
		document.ErrNotReady:                 connect.CodeFailedPrecondition,
		document.ErrOriginalGone:             connect.CodeFailedPrecondition,
		document.ErrCannotAdd:                connect.CodePermissionDenied,
		errNotFound:                          connect.CodeNotFound,
	} {
		s.SetupTest()
		s.copies.EXPECT().Start(mock.Anything, mock.Anything).Return(0, refusal)
		_, err := s.client.Copy(s.T().Context(), connect.NewRequest(&documentv1.CopyRequest{
			DocId: "pager", Library: "platform",
		}))
		s.Equal(want, connect.CodeOf(err), refusal.Error())
	}
}
