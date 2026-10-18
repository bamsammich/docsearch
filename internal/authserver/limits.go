package authserver

import (
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/httprate"
)

// Rates the open doors are held to, per address. Each is several times what
// a person connecting an app uses, and far below what fills a table or keeps
// the password check busy. Behind a reverse proxy every caller has the
// proxy's address, so the limits apply to the deployment as a whole; a
// forwarded header is not trusted for this, since whoever sends it would
// choose their own bucket.
const (
	registrationsPerHour = 30
	authorizesPerMinute  = 60
	signInsPerMinute     = 20
)

// Limits on an authorization request, which the server stores until it
// expires. A real client's are a fraction of these.
const (
	maxAuthorizeQuery = 8 << 10
	maxStateBytes     = 1 << 10
	// A PKCE verifier is 43 to 128 characters, and its S256 challenge is
	// always 43.
	challengeLength = 43
)

// limited holds next to n requests per window from any one address.
func limited(n int, window time.Duration, next http.Handler) http.Handler {
	return httprate.LimitBy(n, window, byPeer)(next)
}

// byPeer is the address the connection came from. Never a header, which a
// caller could set to anything.
func byPeer(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return httprate.CanonicalizeIP(host), nil
}

// authorize admits an authorization request the provider would accept, and
// gives one that names no scope the scope docsearch grants.
//
// The provider sends most refusals back to the client's redirect URI. Any
// caller can register a client with any https redirect URI, so a link to
// docsearch's own authorize endpoint would then forward a browser wherever
// its author chose. A request that is malformed is refused here, on
// docsearch's page, instead.
func authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "send an authorization request as a GET", http.StatusMethodNotAllowed)
			return
		}
		if reason := checkAuthorize(r.URL); reason != "" {
			http.Error(w, reason, http.StatusBadRequest)
			return
		}
		query := r.URL.Query()
		if query.Get("scope") == "" {
			// The provider refuses a request with no scope, and an MCP
			// client told no scope by the resource server may send none.
			query.Set("scope", Scope)
			r.URL.RawQuery = query.Encode()
		}
		next.ServeHTTP(w, r)
	})
}

// checkAuthorize is why an authorization request is refused, or "".
func checkAuthorize(u *url.URL) string {
	if len(u.RawQuery) > maxAuthorizeQuery {
		return "the authorization request is too long"
	}
	query := u.Query()
	switch {
	case query.Get("response_type") != "code":
		return "docsearch issues codes only: send response_type=code"
	case query.Get("code_challenge_method") != "S256" ||
		len(query.Get("code_challenge")) != challengeLength:
		return "docsearch requires PKCE: send an S256 code_challenge"
	case len(query.Get("state")) > maxStateBytes || len(query.Get("nonce")) > maxStateBytes:
		return "state and nonce may be at most 1024 bytes"
	}
	return ""
}
