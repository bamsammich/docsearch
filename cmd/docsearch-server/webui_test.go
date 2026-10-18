package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
)

// WebUISuite covers how the library UI sits beside the guarded routes: served
// with no credential, and posting from an origin the server lets in.
type WebUISuite struct {
	suite.Suite
	mux *http.ServeMux
}

func TestWebUI(t *testing.T) { suite.Run(t, new(WebUISuite)) }

const documentList = "/docsearch.document.v1.DocumentService/List"

func (s *WebUISuite) SetupTest() {
	cfg := config.Config{
		Auth:        owner.Token,
		BearerToken: "right",
		PublicURL:   "http://127.0.0.1:8765",
		UploadMaxMB: 100,
	}
	guarded := route{
		path: "/docsearch.document.v1.DocumentService/",
		handler: http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		),
	}
	guard := access{guard: func(next http.Handler) http.Handler { return authenticated(cfg, next) }}
	s.mux = newMux([]route{guarded}, cfg, nil, guard, uploads{}, newWebUI(cfg), nil)
}

func (s *WebUISuite) do(method, path, origin, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(s.T().Context(), method, path, strings.NewReader("{}"))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

func (s *WebUISuite) TestTheConfigNeedsNoCredential() {
	rec := s.do(http.MethodGet, "/ui/config", "", "")
	s.Equal(http.StatusOK, rec.Code)
	s.JSONEq(`{"auth":"token","upload_max_mb":100,"public_url":"http://127.0.0.1:8765",`+
		`"origin_allowed":false}`, rec.Body.String())
}

func (s *WebUISuite) TestThePageNeedsNoCredential() {
	rec := s.do(http.MethodGet, "/", "", "")
	s.NotEqual(http.StatusUnauthorized, rec.Code)
	s.NotEqual(http.StatusNotFound, rec.Code)
}

func (s *WebUISuite) TestTheServersOwnOriginIsLetIn() {
	rec := s.do(http.MethodPost, documentList, "http://127.0.0.1:8765", "right")
	s.Equal(http.StatusOK, rec.Code)
}

func (s *WebUISuite) TestTheOwnOriginStillNeedsTheCredential() {
	rec := s.do(http.MethodPost, documentList, "http://127.0.0.1:8765", "")
	s.Equal(http.StatusUnauthorized, rec.Code)
}

func (s *WebUISuite) TestAnotherOriginIsStillRefused() {
	rec := s.do(http.MethodPost, documentList, "https://rebinding.example", "right")
	s.Equal(http.StatusForbidden, rec.Code)
}

func (s *WebUISuite) TestPasswordModeNamesDocsearchAsTheIssuer() {
	ui := newWebUI(config.Config{Auth: owner.Password, PublicURL: "https://docs.example.org/"})
	mux := http.NewServeMux()
	for _, r := range ui.routes {
		mux.Handle(r.path, r.handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(
		rec,
		httptest.NewRequestWithContext(s.T().Context(), http.MethodGet, "/ui/config", nil),
	)

	s.JSONEq(`{"auth":"password","issuer":"https://docs.example.org",`+
		`"resource":"https://docs.example.org/mcp","scopes":["docsearch"],"upload_max_mb":0,`+
		`"public_url":"https://docs.example.org/","origin_allowed":false}`,
		rec.Body.String())
}
