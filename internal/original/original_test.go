package original_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/original"
	"github.com/bamsammich/docsearch/internal/original/mocks"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/upload"
)

const user = "user-a"

var errNotFound = errors.New("no such document")

// OriginalSuite serves originals from a mocked library and a real root.
type OriginalSuite struct {
	suite.Suite
	docs    *mocks.MockDocuments
	uploads *mocks.MockUploads
	root    string
}

func TestOriginal(t *testing.T) { suite.Run(t, new(OriginalSuite)) }

func (s *OriginalSuite) SetupTest() {
	s.docs = mocks.NewMockDocuments(s.T())
	s.uploads = mocks.NewMockUploads(s.T())
	s.root = s.T().TempDir()
}

func (s *OriginalSuite) serve(docID string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle(original.Pattern, original.Handler{
		Documents: func(string) (original.Documents, error) { return s.docs, nil },
		Uploads:   func(string) (original.Uploads, error) { return s.uploads, nil },
		NotFound:  errNotFound,
		Log:       slog.New(slog.DiscardHandler),
		Roots:     []string{s.root},
	})
	request := httptest.NewRequestWithContext(owner.With(s.T().Context(), user),
		http.MethodGet, "/original/"+docID, nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func (s *OriginalSuite) holds(doc document.Document) {
	s.docs.EXPECT().Get(mock.Anything, doc.DocID).Return(&doc, nil)
}

func (s *OriginalSuite) TestAnUploadedPDFOpensAsAPDF() {
	s.holds(document.Document{DocID: "manual", Source: upload.Target("k1")})
	s.uploads.EXPECT().Get(mock.Anything, "k1").
		Return(upload.File{Key: "k1", Filename: "manual.pdf", Content: []byte("%PDF-1.7")}, nil)

	got := s.serve("manual")
	s.Equal(http.StatusOK, got.Code)
	s.Equal("application/pdf", got.Header().Get("Content-Type"))
	s.Equal(`inline; filename=manual.pdf`, got.Header().Get("Content-Disposition"))
	s.Equal("sandbox", got.Header().Get("Content-Security-Policy"))
	s.Equal("%PDF-1.7", got.Body.String())
}

// An HTML file served as HTML would run its scripts under the library's
// origin.
func (s *OriginalSuite) TestAnUploadedHTMLFileOpensAsText() {
	s.holds(document.Document{DocID: "page", Source: upload.Target("k2")})
	s.uploads.EXPECT().Get(mock.Anything, "k2").
		Return(upload.File{Filename: "page.html", Content: []byte("<script>x</script>")}, nil)

	got := s.serve("page")
	s.Equal("text/plain; charset=utf-8", got.Header().Get("Content-Type"))
	s.Equal("nosniff", got.Header().Get("X-Content-Type-Options"))
}

func (s *OriginalSuite) TestAWordFileDownloads() {
	s.holds(document.Document{DocID: "spec", Source: upload.Target("k3")})
	s.uploads.EXPECT().Get(mock.Anything, "k3").
		Return(upload.File{Filename: "spec.docx", Content: []byte("PK")}, nil)

	s.Equal(`attachment; filename=spec.docx`, s.serve("spec").Header().Get("Content-Disposition"))
}

func (s *OriginalSuite) TestAFileInTheLibraryRootIsRead() {
	path := filepath.Join(s.root, "notes.md")
	s.Require().NoError(os.WriteFile(path, []byte("# Notes"), 0o600))
	s.holds(document.Document{DocID: "notes", Source: path})

	got := s.serve("notes")
	s.Equal(http.StatusOK, got.Code)
	s.Equal("# Notes", got.Body.String())
}

// The stored path is checked again, so a row that names a file outside every
// root reads nothing.
func (s *OriginalSuite) TestAFileOutsideTheRootsIsNotRead() {
	outside := filepath.Join(s.T().TempDir(), "secret.txt")
	s.Require().NoError(os.WriteFile(outside, []byte("secret"), 0o600))
	s.holds(document.Document{DocID: "secret", Source: outside})

	got := s.serve("secret")
	s.Equal(http.StatusNotFound, got.Code)
	s.NotContains(got.Body.String(), "secret\n")
}

func (s *OriginalSuite) TestAnUploadThatIsGoneIsNotFound() {
	s.holds(document.Document{DocID: "manual", Source: upload.Target("k1")})
	s.uploads.EXPECT().Get(mock.Anything, "k1").Return(upload.File{}, upload.ErrNotFound)

	s.Equal(http.StatusNotFound, s.serve("manual").Code)
}

func (s *OriginalSuite) TestADocumentTheLibraryDoesNotHoldIsNotFound() {
	s.docs.EXPECT().Get(mock.Anything, "gone").Return(nil, errNotFound)

	s.Equal(http.StatusNotFound, s.serve("gone").Code)
}

// A site's original is its URL; the server holds no copy to send.
func (s *OriginalSuite) TestASiteHasNoStoredOriginal() {
	s.holds(document.Document{
		DocID:      "guide",
		Source:     "https://docs.example.dev/guide/",
		SourceKind: domain.SourceKindSite,
	})

	got := s.serve("guide")
	s.Equal(http.StatusNotFound, got.Code)
	s.Empty(got.Header().Get("Location"))
}

func (s *OriginalSuite) TestARequestWithNoOwnerIsRefused() {
	mux := http.NewServeMux()
	mux.Handle(original.Pattern, original.Handler{Log: slog.New(slog.DiscardHandler)})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequestWithContext(s.T().Context(),
		http.MethodGet, "/original/manual", nil))
	s.Equal(http.StatusForbidden, recorder.Code)
}
