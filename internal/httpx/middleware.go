// Package httpx holds the transport-level access controls.
package httpx

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// RequireBearer rejects any request without a matching bearer token, before
// any handler runs.
//
// The comparison is constant time AND always performed, even when the header
// is missing entirely. Returning early on a missing header would make "no
// token" measurably faster than "wrong token", which hands an attacker a
// free oracle for whether they have the shape of a credential right.
//
// Tokens are compared as SHA-256 digests so the comparison is over
// fixed-length inputs; ConstantTimeCompare returns 0 immediately for
// mismatched lengths, which would otherwise leak the expected token's length.
func RequireBearer(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := ""
		if h := r.Header.Get("Authorization"); h != "" {
			if after, ok := strings.CutPrefix(h, "Bearer "); ok {
				presented = after
			} else if after, ok := strings.CutPrefix(h, "bearer "); ok {
				presented = after
			}
		}
		got := sha256.Sum256([]byte(presented))
		if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="docsearch"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// RequireAllowedOrigin enforces the Origin allowlist.
//
// This is a DNS-rebinding defense and applies even on a private network: a
// browser on the same network can POST to this endpoint, so without it a
// visited web page can drive the server.
//
// The absent-Origin case is a deliberate allow, not an accident of the
// condition ordering. Origin is a browser-supplied header; native MCP clients
// do not send one, and rejecting its absence would break every real client
// while stopping no browser. The check below is written as an explicit
// three-way decision rather than as `origin != "" && !allowed` so the choice
// cannot be silently inverted by a later edit. Both branches are pinned by
// tests.
func RequireAllowedOrigin(allowed []string, next http.Handler) http.Handler {
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		set[strings.ToLower(strings.TrimSpace(o))] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		switch {
		case origin == "":
			// No Origin: not a browser. Allowed, deliberately.
			next.ServeHTTP(w, r)
		case set[strings.ToLower(origin)]:
			// Browser origin on the allowlist.
			next.ServeHTTP(w, r)
		default:
			// Browser origin not on the allowlist: this is the rebinding case.
			http.Error(w, "forbidden origin", http.StatusForbidden)
		}
	})
}

// LogRequests emits one structured line per request.
//
// It records method, path, tool name, duration and outcome. It never records
// the Authorization header or the request body, so a bearer token and the
// full text of a query stay out of the log.
func LogRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"tool", r.Header.Get("MCP-Tool-Name"),
			"session", r.Header.Get("Mcp-Session-Id"),
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer so Server-Sent Events still stream.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
