// Package resource makes docsearch an OAuth protected resource.
//
// The operator's identity provider is the authorization server, which is what
// docs/research/mcp-authorization.md settled, so docsearch never issues a
// token here. It reads one, checks that it was issued for docsearch, and
// publishes the metadata that tells a client where to get one. The MCP SDK
// supplies the 401 and 403 shapes and the metadata endpoint; this package
// supplies the decision about whether a token is good.
//
// A token reaches docsearch in one of two forms. A signed JWT is read
// locally against the issuer's published keys, and an opaque token is sent
// back to the issuer for introspection. Both end as the same Claims, so
// every check after that point is written once.
package resource

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Claims are what an issuer vouches for about one token, whichever form the
// token arrived in.
type Claims struct {
	Expiry time.Time
	// Stated is every claim as the issuer stated it, these included, for a
	// policy that reads a claim docsearch has no field for.
	Stated   map[string]any
	Issuer   string
	Subject  string
	Audience []string
	Scopes   []string
}

// Reader turns a bearer token into the claims its issuer vouches for.
//
// A token the issuer does not vouch for is an error wrapping
// auth.ErrInvalidToken, which answers 401. Any other error means the token
// could not be checked at all, such as an issuer that did not answer, and
// answers 500 rather than telling a client its credential is bad.
type Reader interface {
	Read(ctx context.Context, token string) (Claims, error)
}

// Identity is who a token names. The pair is the account, because two
// issuers can each have a subject called alice.
type Identity struct {
	Issuer  string
	Subject string
}

// Config is what an operator says about their provider.
type Config struct {
	// Issuer is the authorization server's issuer identifier, exactly as its
	// discovery document spells it.
	Issuer string
	// Resource is docsearch's own URI as a client addresses it, such as
	// https://docs.example.com/mcp. Clients bind tokens to it with the
	// resource parameter of RFC 8707.
	Resource string
	// Audience is the aud value a token must carry. Empty means Resource.
	// It is separate because a provider that ignores the resource parameter
	// may bind an audience of its own, such as a project identifier.
	Audience string
	// ScopesSupported is advertised in the metadata. A client given no scope
	// by a challenge requests these, which is how an operator asks for a
	// provider-specific scope that binds the audience.
	ScopesSupported []string
	// RequiredScopes must all be on a token, and are named in every
	// challenge. A token without one is answered 403.
	RequiredScopes []string
}

// metadataPrefix is where RFC 9728 section 3.1 puts protected resource
// metadata: inserted between the host and the resource's path.
const metadataPrefix = "/.well-known/oauth-protected-resource"

// identityKey and statedKey are where Verify leaves the identity and the
// stated claims in TokenInfo.Extra, the one place the SDK carries anything
// from a verifier to a handler.
const (
	identityKey = "docsearch.identity"
	statedKey   = "docsearch.stated"
)

// errUnchecked replaces whatever stopped a token being checked, which can
// name the issuer's internal addresses and does not belong in a response.
var errUnchecked = errors.New("the token could not be checked")

// Server is docsearch as a protected resource.
type Server struct {
	signed   Reader
	opaque   Reader
	log      *slog.Logger
	metadata url.URL
	cfg      Config
}

// New checks the configuration and returns the resource server.
//
// signed reads JWTs and opaque reads everything else. A deployment that
// introspects nothing passes Unintrospected as opaque, so an opaque token is
// refused by name rather than read as a malformed JWT.
func New(cfg Config, signed, opaque Reader, log *slog.Logger) (*Server, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("resource server: no issuer")
	}
	metadata, err := metadataLocation(cfg.Resource)
	if err != nil {
		return nil, err
	}
	if cfg.Audience == "" {
		cfg.Audience = cfg.Resource
	}
	return &Server{
		cfg: cfg, signed: signed, opaque: opaque, metadata: metadata, log: log,
	}, nil
}

// metadataLocation is the metadata URL for a resource, per RFC 9728 section
// 3.1. The resource must be absolute and carry no fragment, which section 2
// requires of a resource identifier.
func metadataLocation(resource string) (url.URL, error) {
	parsed, err := url.Parse(resource)
	if err != nil {
		return url.URL{}, fmt.Errorf("resource server: resource %q: %w", resource, err)
	}
	if !parsed.IsAbs() || parsed.Host == "" || parsed.Fragment != "" {
		return url.URL{}, fmt.Errorf(
			"resource server: resource %q must be an absolute URL with no fragment", resource)
	}
	return url.URL{
		Scheme: parsed.Scheme,
		Host:   parsed.Host,
		Path:   metadataPrefix + strings.TrimSuffix(parsed.Path, "/"),
	}, nil
}

// MetadataURL is where a client fetches the protected resource metadata, and
// what every challenge names.
func (s *Server) MetadataURL() string { return s.metadata.String() }

// MetadataPath is MetadataURL's path, for mounting the handler.
func (s *Server) MetadataPath() string { return s.metadata.Path }

// Metadata serves the RFC 9728 document naming the issuer.
func (s *Server) Metadata() http.Handler {
	return auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               s.cfg.Resource,
		AuthorizationServers:   []string{s.cfg.Issuer},
		ScopesSupported:        s.cfg.ScopesSupported,
		BearerMethodsSupported: []string{"header"},
	})
}

// Protect refuses a request without a token issued for docsearch, and hands
// on the rest with their TokenInfo in the context.
func (s *Server) Protect(next http.Handler) http.Handler {
	return auth.RequireBearerToken(s.Verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: s.MetadataURL(),
		Scopes:              s.cfg.RequiredScopes,
	})(next)
}

// Verify is the SDK's TokenVerifier.
//
// Scopes and expiry are left to the SDK's middleware, which checks both after
// this returns. Everything about whose token it is and who it was for is
// decided here.
func (s *Server) Verify(
	ctx context.Context,
	token string,
	_ *http.Request,
) (*auth.TokenInfo, error) {
	// A JWS compact serialization is three segments. A JWE has five, and
	// goes to the issuer like any other token docsearch cannot read itself.
	reader := s.opaque
	if strings.Count(token, ".") == 2 {
		reader = s.signed
	}
	claims, err := reader.Read(ctx, token)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidToken) {
			return nil, err
		}
		s.log.ErrorContext(ctx, "bearer token not checked", "err", err)
		return nil, errUnchecked
	}
	if err := s.check(claims); err != nil {
		return nil, err
	}
	return &auth.TokenInfo{
		Scopes:     claims.Scopes,
		Expiration: claims.Expiry,
		Extra: map[string]any{
			identityKey: Identity{Issuer: claims.Issuer, Subject: claims.Subject},
			statedKey:   claims.Stated,
		},
	}, nil
}

// check holds the claims to this deployment.
//
// The audience is the check worth reading twice. A token another service was
// issued, presented here, carries the same issuer and a real subject; only
// the audience says it was not meant for docsearch, and accepting it would
// make docsearch a confused deputy against that service.
func (s *Server) check(claims Claims) error {
	if claims.Issuer != s.cfg.Issuer {
		return fmt.Errorf("%w: issued by %q", auth.ErrInvalidToken, claims.Issuer)
	}
	if claims.Subject == "" {
		return fmt.Errorf("%w: names no subject", auth.ErrInvalidToken)
	}
	if !oauthex.MatchesResource(claims.Audience, s.cfg.Audience) {
		return fmt.Errorf("%w: issued for another resource", auth.ErrInvalidToken)
	}
	return nil
}

// IdentityOf is who a verified token names.
func IdentityOf(info *auth.TokenInfo) (Identity, error) {
	if info == nil {
		return Identity{}, errors.New("no token info: the request did not pass Protect")
	}
	identity, ok := info.Extra[identityKey].(Identity)
	if !ok {
		return Identity{}, errors.New("token info carries no identity")
	}
	return identity, nil
}

// Stated is the claim called name on a verified token, as the issuer stated
// it.
func Stated(info *auth.TokenInfo, name string) (any, bool) {
	if info == nil {
		return nil, false
	}
	stated, ok := info.Extra[statedKey].(map[string]any)
	if !ok {
		return nil, false
	}
	value, ok := stated[name]
	return value, ok
}

// Unintrospected is the opaque Reader for a deployment with no introspection
// configured. Its refusal names the cause, because "malformed token" would
// send an operator looking at the token rather than at the configuration.
type Unintrospected struct{}

// Read refuses every token.
func (Unintrospected) Read(context.Context, string) (Claims, error) {
	return Claims{}, fmt.Errorf(
		"%w: not a JWT, and no introspection is configured to read it", auth.ErrInvalidToken)
}
