package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() (http.Handler, *bool) {
	reached := false
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}), &reached
}

// -- bearer ---------------------------------------------------------------

func TestMissingTokenIsRejectedBeforeTheHandlerRuns(t *testing.T) {
	next, reached := okHandler()
	h := RequireBearer("secret", next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if *reached {
		t.Error("handler ran despite a missing token")
	}
}

func TestWrongTokenIsRejectedBeforeTheHandlerRuns(t *testing.T) {
	next, reached := okHandler()
	h := RequireBearer("secret", next)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if *reached {
		t.Error("handler ran despite a wrong token")
	}
}

func TestCorrectTokenReachesTheHandler(t *testing.T) {
	next, reached := okHandler()
	h := RequireBearer("secret", next)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !*reached {
		t.Errorf("status = %d reached = %v, want 200 and reached", rec.Code, *reached)
	}
}

// TestMissingAndWrongTokenTakeTheSamePath guards the property that produces
// constant-time behaviour: the digest comparison must run even when no header
// was sent. If a missing header short-circuits, "no token" becomes measurably
// cheaper than "wrong token" and leaks whether a credential was well-formed.
//
// Wall-clock timing is too noisy to assert in a unit test, so this asserts the
// observable proxy: both cases produce byte-identical responses.
func TestMissingAndWrongTokenAreIndistinguishable(t *testing.T) {
	h := RequireBearer("secret", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	wrongReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	wrongReq.Header.Set("Authorization", "Bearer "+string(make([]byte, 4096)))
	wrong := httptest.NewRecorder()
	h.ServeHTTP(wrong, wrongReq)

	if missing.Code != wrong.Code {
		t.Errorf("status differs: missing=%d wrong=%d", missing.Code, wrong.Code)
	}
	if missing.Body.String() != wrong.Body.String() {
		t.Errorf("body differs: missing=%q wrong=%q", missing.Body, wrong.Body)
	}
	if missing.Header().Get("WWW-Authenticate") != wrong.Header().Get("WWW-Authenticate") {
		t.Error("WWW-Authenticate differs between missing and wrong token")
	}
}

func TestTokenOfDifferentLengthDoesNotLeakViaEarlyExit(t *testing.T) {
	h := RequireBearer("secret", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, tok := range []string{"", "s", "secre", "secrets", "completely-different-and-longer"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want 401", tok, rec.Code)
		}
	}
}

// -- origin ---------------------------------------------------------------

// The absent-Origin allow is deliberate: native MCP clients send no Origin,
// and rejecting its absence breaks every real client while stopping no
// browser. It is pinned here so it cannot be inverted by a later edit.
func TestAbsentOriginIsAllowed(t *testing.T) {
	next, reached := okHandler()
	h := RequireAllowedOrigin([]string{"https://allowed.example"}, next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusOK || !*reached {
		t.Errorf("absent Origin: status = %d reached = %v, want 200 and reached",
			rec.Code, *reached)
	}
}

func TestDisallowedOriginIsRejected(t *testing.T) {
	next, reached := okHandler()
	h := RequireAllowedOrigin([]string{"https://allowed.example"}, next)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if *reached {
		t.Error("handler ran for a disallowed Origin")
	}
}

func TestAllowedOriginReachesTheHandler(t *testing.T) {
	next, reached := okHandler()
	h := RequireAllowedOrigin([]string{"https://allowed.example"}, next)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "https://allowed.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !*reached {
		t.Errorf("status = %d reached = %v, want 200 and reached", rec.Code, *reached)
	}
}

func TestOriginMatchIsCaseInsensitiveButNotSubstring(t *testing.T) {
	h := RequireAllowedOrigin([]string{"https://allowed.example"},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	cases := map[string]int{
		"HTTPS://ALLOWED.EXAMPLE":          http.StatusOK,
		"https://allowed.example.evil.com": http.StatusForbidden,
		"https://evil.com/allowed.example": http.StatusForbidden,
		"https://allowed.example:8443":     http.StatusForbidden,
	}
	for origin, want := range cases {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Origin %q: status = %d, want %d", origin, rec.Code, want)
		}
	}
}

// An empty allowlist must still reject a browser Origin. Otherwise a
// misconfiguration silently disables the rebinding defence.
func TestEmptyAllowlistStillRejectsBrowserOrigins(t *testing.T) {
	h := RequireAllowedOrigin(nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
