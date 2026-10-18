package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/suite"
)

type WebUISuite struct {
	suite.Suite
}

func TestWebUI(t *testing.T) { suite.Run(t, new(WebUISuite)) }

func (s *WebUISuite) serve(files fstest.MapFS, cfg Config, path string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	for _, r := range Routes(cfg, files) {
		mux.Handle(r.Path, r.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(s.T().Context(), http.MethodGet, path, nil))
	return rec
}

func (s *WebUISuite) TestPageCarriesAPolicyNamingTheIssuer() {
	files := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=app></div>")}}
	rec := s.serve(files, Config{Auth: "oidc", Issuer: "https://id.example.org/realm"}, "/")

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Body.String(), `<div id=app>`)
	policy := rec.Header().Get("Content-Security-Policy")
	s.Contains(policy, "connect-src 'self' https://id.example.org;")
	s.Contains(policy, "script-src 'self';")
	s.Contains(policy, "frame-ancestors 'none'")
	s.Equal("no-cache", rec.Header().Get("Cache-Control"))
}

func (s *WebUISuite) TestPolicyReachesOnlyThisServerWithoutAnIssuer() {
	files := fstest.MapFS{"index.html": {Data: []byte("x")}}
	rec := s.serve(files, Config{Auth: "token"}, "/")

	s.Contains(rec.Header().Get("Content-Security-Policy"), "connect-src 'self';")
}

func (s *WebUISuite) TestUnbuiltAppSaysHowToBuildIt() {
	rec := s.serve(fstest.MapFS{".gitkeep": {}}, Config{Auth: "token"}, "/")

	s.Equal(http.StatusServiceUnavailable, rec.Code)
	s.Contains(rec.Body.String(), "mise run web")
}

func (s *WebUISuite) TestAssetsAreCachedForGood() {
	files := fstest.MapFS{"assets/index-abc.js": {Data: []byte("console.log(1)")}}
	rec := s.serve(files, Config{}, "/assets/index-abc.js")

	s.Equal(http.StatusOK, rec.Code)
	s.Contains(rec.Header().Get("Cache-Control"), "immutable")
}

func (s *WebUISuite) TestOnlyTheRootServesThePage() {
	files := fstest.MapFS{"index.html": {Data: []byte("x")}}
	rec := s.serve(files, Config{}, "/not-a-route")

	s.Equal(http.StatusNotFound, rec.Code)
}

func (s *WebUISuite) TestConfigDescribesTheServer() {
	cfg := Config{
		Auth: "oidc", Issuer: "https://id.example.org", ClientID: "library-ui",
		Scopes: []string{"docsearch"}, Resource: "https://docs.example.org/mcp", UploadMaxMB: 100,
		PublicURL: "https://docs.example.org", Origins: []string{"https://docs.example.org"},
	}
	rec := s.serve(fstest.MapFS{}, cfg, ConfigPath)

	s.Equal(http.StatusOK, rec.Code)
	s.Equal("no-store", rec.Header().Get("Cache-Control"))
	var got map[string]any
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &got))
	s.Equal(map[string]any{
		"auth":      "oidc",
		"issuer":    "https://id.example.org",
		"client_id": "library-ui",
		"scopes": []any{
			"docsearch",
		},
		"resource":       "https://docs.example.org/mcp",
		"upload_max_mb":  float64(100),
		"public_url":     "https://docs.example.org",
		"origin_allowed": false,
	}, got)
}

func (s *WebUISuite) TestEmbeddedFilesOpen() {
	// Whether or not the app was built, the embedded tree is readable.
	_, err := Built().Open(".gitkeep")
	s.Require().NoError(err)
}
