package authserver

import (
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// Paths the server answers on. Everything the provider serves sits under
// /oauth/, so none of it can collide with docsearch's own routes.
const (
	pathRegister   = "/register"
	pathLogin      = "/login"
	pathAuthorize  = "/oauth/authorize"
	providerPrefix = "/oauth/"
)

// metadataPaths are where a client looks for the server's metadata: RFC
// 8414's document first, then OpenID Connect's.
var metadataPaths = []string{
	"/.well-known/oauth-authorization-server",
	"/.well-known/openid-configuration",
}

// Config is what the server needs to know about where it is.
type Config struct {
	// Issuer is the server's own public URL, which tokens name as `iss`.
	Issuer string
	// Resource is docsearch's `/mcp` URI, which tokens name as `aud`.
	Resource string
}

// Route is one path the server answers on, mounted with no credential: each
// is how a client gets one.
type Route struct {
	Handler http.Handler
	Path    string
}

// Server is the authorization server.
type Server struct {
	provider *op.Provider
	// signIn is the POST that checks a password, held to its rate.
	signIn   http.Handler
	storage  *storage
	store    Store
	accounts Accounts
	log      *slog.Logger
	now      func() time.Time
	cfg      Config
}

// New builds the server. It reads the signing keys at once, making the
// first on a database that has none, so a server that cannot sign fails at
// startup rather than at someone's first sign-in.
func New(
	ctx context.Context,
	cfg Config,
	store Store,
	accounts Accounts,
	log *slog.Logger,
) (*Server, error) {
	issuer, err := url.Parse(cfg.Issuer)
	if err != nil || issuer.Host == "" {
		return nil, fmt.Errorf("authorization server: issuer %q is not an absolute URL", cfg.Issuer)
	}
	s := &Server{store: store, accounts: accounts, log: log, now: time.Now, cfg: cfg}
	s.signIn = limited(signInsPerMinute, time.Minute, http.HandlerFunc(s.checkSignIn))
	s.storage = &storage{
		store:    store,
		accounts: accounts,
		now:      s.now,
		login:    func(id string) string { return pathLogin + "?authRequestID=" + url.QueryEscape(id) },
		keys:     &keyring{store: store},
		audience: []string{cfg.Resource},
	}
	keys, err := s.storage.keys.load(ctx)
	if err != nil {
		return nil, err
	}
	options := []op.Option{op.WithCustomEndpoints(
		op.NewEndpoint("oauth/authorize"), op.NewEndpoint("oauth/token"),
		op.NewEndpoint("oauth/userinfo"), op.NewEndpoint("oauth/revoke"),
		op.NewEndpoint("oauth/end_session"), op.NewEndpoint("oauth/keys"),
	), op.WithCustomIntrospectionEndpoint(op.NewEndpoint("oauth/introspect")),
		op.WithCustomDeviceAuthorizationEndpoint(op.NewEndpoint("oauth/device_authorization"))}
	if issuer.Scheme == "http" {
		// Only a loopback deployment is reached over http, and the provider
		// refuses an http issuer unless told this is one.
		options = append(options, op.WithAllowInsecure())
	}
	s.provider, err = op.NewProvider(&op.Config{
		CryptoKey:             cryptoKey(keys[0]),
		CodeMethodS256:        true,
		GrantTypeRefreshToken: true,
		SupportedScopes:       []string{Scope, oidc.ScopeOfflineAccess, oidc.ScopeOpenID},
	}, s.storage, op.StaticIssuer(strings.TrimSuffix(cfg.Issuer, "/")), options...)
	if err != nil {
		return nil, fmt.Errorf("authorization server: %w", err)
	}
	return s, nil
}

// cryptoKey is the provider's key for anything it encrypts, derived from the
// signing key so every server sharing the database agrees on it.
func cryptoKey(key parsedKey) [32]byte {
	der, err := x509.MarshalPKCS8PrivateKey(key.key)
	if err != nil {
		return sha256.Sum256([]byte(key.id))
	}
	return sha256.Sum256(append([]byte("docsearch-crypto-key:"), der...))
}

// Routes are every path the server answers on.
func (s *Server) Routes() []Route {
	routes := []Route{
		{Path: providerPrefix, Handler: s.provider},
		{Path: pathAuthorize, Handler: limited(authorizesPerMinute, time.Minute,
			authorize(s.provider))},
		{Path: pathRegister, Handler: limited(registrationsPerHour, time.Hour,
			http.HandlerFunc(s.register))},
		{Path: pathLogin, Handler: http.HandlerFunc(s.login)},
	}
	for _, path := range metadataPaths {
		routes = append(routes, Route{Path: path, Handler: http.HandlerFunc(s.metadata)})
	}
	return routes
}

// metadata is the server's RFC 8414 document: the provider's own, with the
// registration endpoint it does not serve, and with what docsearch's
// clients may actually use.
func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	ctx := op.ContextWithIssuer(r.Context(), s.provider.IssuerFromRequest(r))
	document := op.CreateDiscoveryConfig(ctx, s.provider, s.storage)
	document.RegistrationEndpoint = strings.TrimSuffix(s.cfg.Issuer, "/") + pathRegister
	document.TokenEndpointAuthMethodsSupported = []oidc.AuthMethod{oidc.AuthMethodNone}
	document.ScopesSupported = []string{Scope, oidc.ScopeOfflineAccess, oidc.ScopeOpenID}
	document.ResponseTypesSupported = []string{string(oidc.ResponseTypeCode)}
	document.GrantTypesSupported = []oidc.GrantType{oidc.GrantTypeCode, oidc.GrantTypeRefreshToken}
	document.CodeChallengeMethodsSupported = []oidc.CodeChallengeMethod{
		oidc.CodeChallengeMethodS256,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := json.NewEncoder(w).Encode(document); err != nil {
		s.log.ErrorContext(r.Context(), "write authorization server metadata", "err", err)
	}
}

// KeySet is the public keys tokens are signed with, for the resource server
// that verifies them.
func (s *Server) KeySet(ctx context.Context) ([]crypto.PublicKey, error) {
	keys, err := s.storage.keys.load(ctx)
	if err != nil {
		return nil, err
	}
	public := make([]crypto.PublicKey, 0, len(keys))
	for _, k := range keys {
		public = append(public, &k.key.PublicKey)
	}
	return public, nil
}
