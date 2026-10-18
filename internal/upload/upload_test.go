package upload_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/upload"
	"github.com/bamsammich/docsearch/internal/upload/mocks"
)

const (
	user  = "user-a"
	token = "TOKEN123"
	pdf   = "%PDF-1.7 a small manual"
)

// UploadSuite drives both doors through real HTTP requests, with the link
// table and the store mocked.
type UploadSuite struct {
	suite.Suite
	links    *mocks.MockLinks
	store    *mocks.MockStore
	enqueued []string
}

func TestUpload(t *testing.T) { suite.Run(t, new(UploadSuite)) }

func (s *UploadSuite) SetupTest() {
	s.links = mocks.NewMockLinks(s.T())
	s.store = mocks.NewMockStore(s.T())
	s.enqueued = nil
}

func (s *UploadSuite) doors() upload.Doors {
	return upload.Doors{
		Links:  s.links,
		Stores: func(string) (upload.Store, error) { return s.store, nil },
		Enqueue: func(_ context.Context, _, target string) (int64, error) {
			s.enqueued = append(s.enqueued, target)
			return 42, nil
		},
		Supports: func(name string) bool { return filepath.Ext(name) == ".pdf" },
		Log:      slog.New(slog.DiscardHandler),
		MaxBytes: 1 << 10,
	}
}

func hashOf(t string) string { return upload.Digest([]byte(t)) }

// linkOpen is a link that still works when checked.
func (s *UploadSuite) linkOpen() {
	s.links.EXPECT().Open(mock.Anything, hashOf(token)).Return(true, nil).Once()
}

func serve(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func (s *UploadSuite) put(name, body string) *http.Request {
	return httptest.NewRequestWithContext(s.T().Context(), http.MethodPut,
		upload.LinkPrefix+token+"?name="+name, strings.NewReader(body))
}

// The table keeps a hash, so the link handed out is the only copy of the
// token that works.
func (s *UploadSuite) TestAnIssuedLinkStoresOnlyItsHash() {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var stored string
	s.links.EXPECT().Create(mock.Anything, user, mock.Anything, now.Add(time.Minute)).
		RunAndReturn(func(_ context.Context, _, hash string, _ time.Time) error {
			stored = hash
			return nil
		})

	link, err := upload.NewIssuer(s.links, "https://docs.example.com/", time.Minute,
		func() time.Time { return now }).Issue(s.T().Context(), user)
	s.Require().NoError(err)

	issued := strings.TrimPrefix(link.URL, "https://docs.example.com"+upload.LinkPrefix)
	s.NotEqual(link.URL, issued, "the link sits under the public URL")
	s.Equal(hashOf(issued), stored)
	s.NotContains(link.URL, stored)
	s.Equal(now.Add(time.Minute), link.Expires)
}

func (s *UploadSuite) TestALinkUploadsOneFileIntoItsUsersLibrary() {
	s.linkOpen()
	s.links.EXPECT().Redeem(mock.Anything, hashOf(token)).Return(user, true, nil)
	s.store.EXPECT().Put(mock.Anything, upload.File{
		Key: hashOf(pdf), Filename: "manual.pdf", SHA256: hashOf(pdf), Content: []byte(pdf),
	}).Return(nil)

	got := serve(s.doors().Link(), s.put("manual.pdf", pdf))
	s.Equal(http.StatusAccepted, got.Code)
	s.JSONEq(`{"key":"`+hashOf(pdf)+`","source":"upload:`+hashOf(pdf)+`","job_id":42}`,
		got.Body.String())
	s.Equal([]string{"upload:" + hashOf(pdf)}, s.enqueued)
}

// A file nobody can read is refused before the link is spent, so the person
// still has the link to try another file with.
func (s *UploadSuite) TestAnUnreadableFileLeavesTheLinkUnspent() {
	s.linkOpen()
	got := serve(s.doors().Link(), s.put("photo.heic", "bytes"))
	s.Equal(http.StatusUnsupportedMediaType, got.Code)
	s.links.AssertNotCalled(s.T(), "Redeem", mock.Anything, mock.Anything)
}

// A link that no longer works is refused before a byte of the body is read,
// so a caller with no link cannot make the server buffer a file.
func (s *UploadSuite) TestAClosedLinkIsRefusedBeforeTheBodyIsRead() {
	s.links.EXPECT().Open(mock.Anything, hashOf(token)).Return(false, nil)
	body := &countingReader{Reader: strings.NewReader(strings.Repeat("x", 512))}
	request := httptest.NewRequestWithContext(s.T().Context(), http.MethodPut,
		upload.LinkPrefix+token+"?name=manual.pdf", body)

	got := serve(s.doors().Link(), request)
	s.Equal(http.StatusNotFound, got.Code)
	s.Contains(got.Body.String(), "expired or was already used")
	s.Zero(body.read, "nothing was read")
	s.links.AssertNotCalled(s.T(), "Redeem", mock.Anything, mock.Anything)
	s.Empty(s.enqueued)
}

// A link that closed between the check and the spend, used by a second
// upload racing this one, is still refused.
func (s *UploadSuite) TestALinkSpentInTheMeantimeIsRefused() {
	s.linkOpen()
	s.links.EXPECT().Redeem(mock.Anything, hashOf(token)).Return("", false, nil)
	got := serve(s.doors().Link(), s.put("manual.pdf", pdf))
	s.Equal(http.StatusNotFound, got.Code)
	s.Empty(s.enqueued)
}

// countingReader counts what was read from it.
type countingReader struct {
	io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.read += n
	return n, err
}

func (s *UploadSuite) TestAFileOverTheLimitIsRefused() {
	s.linkOpen()
	got := serve(s.doors().Link(), s.put("manual.pdf", strings.Repeat("x", 2<<10)))
	s.Equal(http.StatusRequestEntityTooLarge, got.Code)
	s.links.AssertNotCalled(s.T(), "Redeem", mock.Anything, mock.Anything)
}

func (s *UploadSuite) TestAPutMustNameItsFile() {
	s.linkOpen()
	request := httptest.NewRequestWithContext(s.T().Context(), http.MethodPut,
		upload.LinkPrefix+token, strings.NewReader(pdf))
	got := serve(s.doors().Link(), request)
	s.Equal(http.StatusBadRequest, got.Code)
	s.Contains(got.Body.String(), "?name=")
}

func (s *UploadSuite) TestThePageOffersAFormOnlyWhileTheLinkWorks() {
	open := httptest.NewRequestWithContext(s.T().Context(), http.MethodGet,
		upload.LinkPrefix+token, nil)

	s.links.EXPECT().Open(mock.Anything, hashOf(token)).Return(true, nil).Once()
	got := serve(s.doors().Link(), open)
	s.Equal(http.StatusOK, got.Code)
	s.Contains(got.Body.String(), `type="file"`)

	s.links.EXPECT().Open(mock.Anything, hashOf(token)).Return(false, nil).Once()
	got = serve(s.doors().Link(), open)
	s.Equal(http.StatusNotFound, got.Code)
	s.NotContains(got.Body.String(), `type="file"`)
}

// The form a browser submits ends on a page, not on JSON.
func (s *UploadSuite) TestABrowserFormEndsOnAPage() {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "manual.pdf")
	s.Require().NoError(err)
	_, err = io.WriteString(part, pdf)
	s.Require().NoError(err)
	s.Require().NoError(form.Close())

	request := httptest.NewRequestWithContext(s.T().Context(), http.MethodPost,
		upload.LinkPrefix+token, &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Accept", "text/html")
	s.linkOpen()
	s.links.EXPECT().Redeem(mock.Anything, hashOf(token)).Return(user, true, nil)
	s.store.EXPECT().Put(mock.Anything, mock.Anything).Return(nil)

	got := serve(s.doors().Link(), request)
	s.Equal(http.StatusAccepted, got.Code)
	s.Contains(got.Body.String(), "job 42")
}

// A store that fails says so in the log, and the caller gets no detail.
func (s *UploadSuite) TestAStoreFailureStaysOutOfTheResponse() {
	s.linkOpen()
	s.links.EXPECT().Redeem(mock.Anything, hashOf(token)).Return(user, true, nil)
	s.store.EXPECT().Put(mock.Anything, mock.Anything).
		Return(errors.New("pq: relation uploads on 10.0.0.7"))
	got := serve(s.doors().Link(), s.put("manual.pdf", pdf))
	s.Equal(http.StatusInternalServerError, got.Code)
	s.NotContains(got.Body.String(), "10.0.0.7")
}

// The authenticated door takes the key a caller names, which is how a sync
// replaces the document it uploaded last time.
func (s *UploadSuite) TestTheDirectDoorUsesTheCallersKey() {
	request := httptest.NewRequestWithContext(
		owner.With(s.T().Context(), user), http.MethodPut,
		"/upload?name=manual.pdf&key=paperless-17", strings.NewReader(pdf))
	s.store.EXPECT().Put(mock.Anything, mock.MatchedBy(func(f upload.File) bool {
		return f.Key == "paperless-17" && f.SHA256 == hashOf(pdf)
	})).Return(nil)

	got := serve(s.doors().Direct(), request)
	s.Equal(http.StatusAccepted, got.Code)
	s.Equal([]string{"upload:paperless-17"}, s.enqueued)
}

func (s *UploadSuite) TestAnUploadTargetNamesItsKey() {
	key, ok := upload.KeyOf(upload.Target("abc"))
	s.True(ok)
	s.Equal("abc", key)
	for _, target := range []string{"upload:", "/library/upload:x", "https://example.com"} {
		_, ok := upload.KeyOf(target)
		s.False(ok, target)
	}
}
