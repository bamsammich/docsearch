// Package signed reads an access token that is a signed JWT, against the
// keys its issuer publishes.
//
// go-oidc does the work: discovery, fetching and rotating the key set, the
// signature, the issuer and the expiry. Its verifier is written for ID tokens,
// whose audience is the client that asked for them, so the client check is
// switched off here and the audience is held to docsearch's own value in
// package resource instead.
package signed

import (
	"context"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/bamsammich/docsearch/internal/resource"
)

// algorithms are every asymmetric algorithm go-oidc verifies.
//
// Without a list, go-oidc takes the algorithms the provider advertises for ID
// tokens, and a provider can sign access tokens with another. Symmetric
// algorithms are absent on purpose: a key shared with the issuer would be a
// key every client of that issuer could sign with.
var algorithms = []string{
	oidc.RS256, oidc.RS384, oidc.RS512,
	oidc.ES256, oidc.ES384, oidc.ES512,
	oidc.PS256, oidc.PS384, oidc.PS512,
	oidc.EdDSA,
}

// Reader reads JWT access tokens from one issuer.
type Reader struct {
	verifier *oidc.IDTokenVerifier
}

// config skips the client check, which compares the audience with an OAuth
// client's ID and belongs to ID tokens. package resource holds the audience
// to docsearch's own value instead.
func config() *oidc.Config {
	return &oidc.Config{SkipClientIDCheck: true, SupportedSigningAlgs: algorithms}
}

// Discover finds the issuer's key set through its discovery document, which
// needs the issuer to answer.
func Discover(ctx context.Context, issuer string) (*Reader, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", issuer, err)
	}
	return &Reader{verifier: provider.Verifier(config())}, nil
}

// New reads tokens from issuer against keys. Discover is the way a
// deployment builds one; New is for a caller that already holds the keys.
func New(issuer string, keys oidc.KeySet) *Reader {
	return &Reader{verifier: oidc.NewVerifier(issuer, keys, config())}
}

// scopeClaim is RFC 9068's scope: one string, space-separated.
type scopeClaim struct {
	Scope string `json:"scope"`
}

// Read verifies the token and returns its claims.
//
// Every failure is an invalid token, including a key set that could not be
// fetched: go-oidc does not tell the two apart, and a client told 401 asks
// for a new token, which is the right next step for both.
func (r *Reader) Read(ctx context.Context, token string) (resource.Claims, error) {
	verified, err := r.verifier.Verify(ctx, token)
	if err != nil {
		return resource.Claims{}, fmt.Errorf("%w: %w", auth.ErrInvalidToken, err)
	}
	var scope scopeClaim
	if err := verified.Claims(&scope); err != nil {
		return resource.Claims{}, fmt.Errorf("%w: %w", auth.ErrInvalidToken, err)
	}
	stated := map[string]any{}
	if err := verified.Claims(&stated); err != nil {
		return resource.Claims{}, fmt.Errorf("%w: %w", auth.ErrInvalidToken, err)
	}
	return resource.Claims{
		Stated:   stated,
		Issuer:   verified.Issuer,
		Subject:  verified.Subject,
		Audience: verified.Audience,
		Scopes:   strings.Fields(scope.Scope),
		Expiry:   verified.Expiry,
	}, nil
}
