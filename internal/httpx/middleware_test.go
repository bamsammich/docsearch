package httpx

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
)

// MiddlewareSuite covers the bearer check, the origin check and the request
// log.
type MiddlewareSuite struct {
	suite.Suite
	// reached is whether the wrapped handler ran.
	reached bool
}

func TestMiddleware(t *testing.T) { suite.Run(t, new(MiddlewareSuite)) }

func (s *MiddlewareSuite) SetupTest() { s.reached = false }

// next is the handler a middleware guards, which records that it ran.
func (s *MiddlewareSuite) next() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.reached = true
		w.WriteHeader(http.StatusOK)
	})
}

// serve sends one POST to /mcp through h, with the headers given.
func serve(h http.Handler, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func origin(value string) map[string]string {
	return map[string]string{"Origin": value}
}

// -- bearer ---------------------------------------------------------------

func (s *MiddlewareSuite) TestMissingTokenIsRejectedBeforeTheHandlerRuns() {
	rec := serve(RequireBearer("secret", s.next()), nil)
	s.Equal(http.StatusUnauthorized, rec.Code)
	s.False(s.reached)
}

func (s *MiddlewareSuite) TestWrongTokenIsRejectedBeforeTheHandlerRuns() {
	rec := serve(RequireBearer("secret", s.next()), bearer("wrong"))
	s.Equal(http.StatusUnauthorized, rec.Code)
	s.False(s.reached)
}

func (s *MiddlewareSuite) TestCorrectTokenReachesTheHandler() {
	rec := serve(RequireBearer("secret", s.next()), bearer("secret"))
	s.Equal(http.StatusOK, rec.Code)
	s.True(s.reached)
}

// This guards the property that produces constant-time behaviour: the digest
// comparison must run even when no header was sent. If a missing header
// short-circuits, "no token" becomes measurably cheaper than "wrong token"
// and leaks whether a credential was well-formed.
//
// Wall-clock timing is too noisy to assert in a unit test, so this asserts the
// observable proxy: both cases produce byte-identical responses.
func (s *MiddlewareSuite) TestMissingAndWrongTokenAreIndistinguishable() {
	h := RequireBearer("secret", s.next())
	missing := serve(h, nil)
	wrong := serve(h, bearer(string(make([]byte, 4096))))
	s.Equal(missing.Code, wrong.Code)
	s.Equal(missing.Body.String(), wrong.Body.String())
	s.Equal(missing.Header().Get("WWW-Authenticate"), wrong.Header().Get("WWW-Authenticate"))
}

func (s *MiddlewareSuite) TestTokenOfDifferentLengthDoesNotLeakViaEarlyExit() {
	h := RequireBearer("secret", s.next())
	for _, tok := range []string{"", "s", "secre", "secrets", "completely-different-and-longer"} {
		s.Equal(http.StatusUnauthorized, serve(h, bearer(tok)).Code, tok)
	}
}

// -- origin ---------------------------------------------------------------

// The absent-Origin allow is deliberate: native MCP clients send no Origin,
// and rejecting its absence breaks every real client while stopping no
// browser. It is pinned here so it cannot be inverted by a later edit.
func (s *MiddlewareSuite) TestAbsentOriginIsAllowed() {
	rec := serve(RequireAllowedOrigin([]string{"https://allowed.example"}, s.next()), nil)
	s.Equal(http.StatusOK, rec.Code)
	s.True(s.reached)
}

func (s *MiddlewareSuite) TestDisallowedOriginIsRejected() {
	rec := serve(RequireAllowedOrigin([]string{"https://allowed.example"}, s.next()),
		origin("https://evil.example"))
	s.Equal(http.StatusForbidden, rec.Code)
	s.False(s.reached)
}

func (s *MiddlewareSuite) TestAllowedOriginReachesTheHandler() {
	rec := serve(RequireAllowedOrigin([]string{"https://allowed.example"}, s.next()),
		origin("https://allowed.example"))
	s.Equal(http.StatusOK, rec.Code)
	s.True(s.reached)
}

func (s *MiddlewareSuite) TestOriginMatchIsCaseInsensitiveButNotSubstring() {
	h := RequireAllowedOrigin([]string{"https://allowed.example"}, s.next())
	for value, want := range map[string]int{
		"HTTPS://ALLOWED.EXAMPLE":          http.StatusOK,
		"https://allowed.example.evil.com": http.StatusForbidden,
		"https://evil.com/allowed.example": http.StatusForbidden,
		"https://allowed.example:8443":     http.StatusForbidden,
	} {
		s.Equal(want, serve(h, origin(value)).Code, value)
	}
}

// An empty allowlist must still reject a browser Origin. Otherwise a
// misconfiguration silently disables the rebinding defence.
func (s *MiddlewareSuite) TestEmptyAllowlistStillRejectsBrowserOrigins() {
	rec := serve(RequireAllowedOrigin(nil, s.next()), origin("https://evil.example"))
	s.Equal(http.StatusForbidden, rec.Code)
}

// -- request log ----------------------------------------------------------

// A signed link's token is its credential, so the request log names the
// route and never the token.
func (s *MiddlewareSuite) TestAnUploadLinkTokenIsNotLogged() {
	var logged bytes.Buffer
	handler := LogRequests(slog.New(slog.NewTextHandler(&logged, nil)), http.NotFoundHandler())
	handler.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPut, "/upload/SECRET-TOKEN", nil))
	s.NotContains(logged.String(), "SECRET-TOKEN")
	s.Contains(logged.String(), "/upload/<redacted>")
}
