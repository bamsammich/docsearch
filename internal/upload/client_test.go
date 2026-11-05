package upload_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/upload"
)

// ClientSuite checks what the upload client asks the door for.
type ClientSuite struct {
	suite.Suite
	asked  url.Values
	server *httptest.Server
}

func TestClient(t *testing.T) { suite.Run(t, new(ClientSuite)) }

func (s *ClientSuite) SetupTest() {
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.asked = r.URL.Query()
		w.WriteHeader(http.StatusAccepted)
		_, err := w.Write([]byte(`{"key":"paperless-1","job_id":7}`))
		s.NoError(err)
	}))
	s.T().Cleanup(s.server.Close)
}

func (s *ClientSuite) TestAnUnscopedClientUploadsIntoYourOwnLibrary() {
	client := upload.NewClient(s.server.Client(), s.server.URL, "token")
	_, err := client.Put(s.T().Context(), "manual.pdf", "paperless-1", strings.NewReader("%PDF"))
	s.Require().NoError(err)

	s.False(s.asked.Has("library"))
	s.Equal("paperless-1", s.asked.Get("key"))
}

func (s *ClientSuite) TestAScopedClientNamesItsLibrary() {
	client := upload.NewClient(s.server.Client(), s.server.URL, "token").In("platform")
	_, err := client.Put(s.T().Context(), "manual.pdf", "paperless-1", strings.NewReader("%PDF"))
	s.Require().NoError(err)

	s.Equal("platform", s.asked.Get("library"))
}
