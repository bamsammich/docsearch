// Package introspect reads an opaque access token by asking its issuer,
// through token introspection as RFC 7662 defines it.
//
// zitadel/oidc's resource-server client does the request: discovery of the
// introspection endpoint, client authentication, and decoding a response
// whose audience may be a string or a list. It is a general OIDC library
// rather than anything tied to one provider.
package introspect

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/zitadel/oidc/v3/pkg/client/rs"
	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/bamsammich/docsearch/internal/resource"
)

// Introspector asks an issuer about one token.
type Introspector interface {
	Introspect(ctx context.Context, token string) (oidc.IntrospectionResponse, error)
}

// client is the Introspector a deployment uses: the request itself, made by
// the library.
type client struct {
	server rs.ResourceServer
}

func (c client) Introspect(ctx context.Context, token string) (oidc.IntrospectionResponse, error) {
	return rs.Introspect[oidc.IntrospectionResponse](ctx, c.server, token)
}

// Reader introspects tokens at one issuer.
type Reader struct {
	introspector Introspector
	issuer       string
}

// Discover finds the issuer's introspection endpoint. clientID and secret are
// docsearch's own credentials at the issuer, which RFC 7662 requires of
// anyone asking about a token.
func Discover(ctx context.Context, issuer, clientID, secret string) (*Reader, error) {
	server, err := rs.NewResourceServerClientCredentials(ctx, issuer, clientID, secret)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", issuer, err)
	}
	return New(issuer, client{server: server}), nil
}

// New reads tokens from issuer through introspector.
func New(issuer string, introspector Introspector) *Reader {
	return &Reader{introspector: introspector, issuer: issuer}
}

// Read asks the issuer about the token.
//
// An inactive token is an invalid one. A failed request is not: the issuer
// said nothing about the token, so the client is not told its credential is
// bad.
func (r *Reader) Read(ctx context.Context, token string) (resource.Claims, error) {
	answer, err := r.introspector.Introspect(ctx, token)
	if err != nil {
		return resource.Claims{}, fmt.Errorf("introspect: %w", err)
	}
	if !answer.Active {
		return resource.Claims{}, fmt.Errorf("%w: inactive", auth.ErrInvalidToken)
	}
	// iss is optional in an introspection response. The endpoint asked
	// belongs to this issuer, so a response without one speaks for it.
	issuer := answer.Issuer
	if issuer == "" {
		issuer = r.issuer
	}
	return resource.Claims{
		Stated:   answer.Claims,
		Issuer:   issuer,
		Subject:  answer.Subject,
		Audience: answer.Audience,
		Scopes:   answer.Scope,
		Expiry:   answer.Expiration.AsTime(),
	}, nil
}
