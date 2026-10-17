package authserver

import (
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Limits on what a client may register. Registration is open, as the MCP
// specification expects, so these keep one caller from storing much.
const (
	maxRegistrationBytes = 64 << 10
	maxRedirectURIs      = 10
	maxClientName        = 100
)

// forbiddenSchemes can run in the page they land on, or read a local file,
// rather than hand a code to an app.
var forbiddenSchemes = map[string]bool{
	"javascript": true, "data": true, "file": true, "vbscript": true, "about": true, "blob": true,
}

// registration is the part of an RFC 7591 request docsearch reads.
type registration struct {
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
}

// registered is the RFC 7591 answer.
type registered struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	IssuedAt                int64    `json:"client_id_issued_at"`
}

// registrationError is RFC 7591's error shape.
type registrationError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// register is the dynamic client registration endpoint. Every client is
// registered public, whatever authentication method it asked for: RFC 7591
// lets a server replace what it does not support, and docsearch issues no
// secrets.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request registration
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegistrationBytes)).
		Decode(&request); err != nil {
		s.refuseRegistration(
			w,
			r,
			"invalid_client_metadata",
			"the body is not a registration request",
		)
		return
	}
	if reason := checkRedirectURIs(request.RedirectURIs); reason != "" {
		s.refuseRegistration(w, r, "invalid_redirect_uri", reason)
		return
	}
	created := Client{
		CreatedAt:    s.now(),
		ID:           rand.Text(),
		Name:         clientName(request.ClientName),
		RedirectURIs: request.RedirectURIs,
	}
	if err := s.store.RegisterClient(r.Context(), created); err != nil {
		s.log.ErrorContext(r.Context(), "register client", "err", err)
		http.Error(w, "the client could not be registered", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(registered{
		ClientID:                created.ID,
		ClientName:              created.Name,
		TokenEndpointAuthMethod: "none",
		RedirectURIs:            created.RedirectURIs,
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		IssuedAt:                created.CreatedAt.Unix(),
	}); err != nil {
		s.log.ErrorContext(r.Context(), "write registration", "err", err)
	}
}

// checkRedirectURIs is why a set of redirect URIs is refused, or "".
func checkRedirectURIs(uris []string) string {
	if len(uris) == 0 || len(uris) > maxRedirectURIs {
		return "register between 1 and 10 redirect_uris"
	}
	for _, uri := range uris {
		if reason := checkRedirectURI(uri); reason != "" {
			return reason
		}
	}
	return ""
}

// checkRedirectURI allows https anywhere, http only to this machine, and an
// app's own scheme, which is how RFC 8252 says a native app receives a code.
func checkRedirectURI(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil || !parsed.IsAbs() {
		return uri + " is not an absolute URI"
	}
	if parsed.Fragment != "" {
		return uri + " has a fragment, which a redirect URI may not"
	}
	switch scheme := strings.ToLower(parsed.Scheme); {
	case scheme == "https":
		return ""
	case scheme == "http":
		if isLoopback(parsed.Hostname()) {
			return ""
		}
		return uri + " is http to another machine; use https"
	case forbiddenSchemes[scheme]:
		return uri + " uses a scheme that cannot receive a code"
	default:
		return ""
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clientName is what the sign-in page shows, kept short and readable.
func clientName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "an unnamed app"
	}
	if utf8.RuneCountInString(name) > maxClientName {
		return string([]rune(name)[:maxClientName])
	}
	return name
}

func (s *Server) refuseRegistration(
	w http.ResponseWriter,
	r *http.Request,
	code, description string,
) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	if err := json.NewEncoder(w).
		Encode(registrationError{Error: code, Description: description}); err != nil {
		s.log.ErrorContext(r.Context(), "write registration refusal", "err", err)
	}
}
