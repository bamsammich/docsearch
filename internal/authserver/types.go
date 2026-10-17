package authserver

import (
	"crypto/ecdsa"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// authRequest is a Request as the provider reads it.
type authRequest struct {
	Request
}

func (r authRequest) GetID() string          { return r.ID }
func (authRequest) GetACR() string           { return "" }
func (r authRequest) GetAudience() []string  { return r.Audience }
func (r authRequest) GetAuthTime() time.Time { return r.AuthTime }
func (r authRequest) GetClientID() string    { return r.ClientID }
func (r authRequest) GetNonce() string       { return r.Nonce }
func (r authRequest) GetRedirectURI() string { return r.RedirectURI }
func (r authRequest) GetScopes() []string    { return r.Scopes }
func (r authRequest) GetState() string       { return r.State }
func (r authRequest) GetSubject() string     { return r.UserID }
func (r authRequest) Done() bool             { return r.UserID != "" }

func (r authRequest) GetAMR() []string {
	if r.Done() {
		return []string{"pwd"}
	}
	return nil
}

func (r authRequest) GetResponseType() oidc.ResponseType {
	return oidc.ResponseType(r.ResponseType)
}

func (r authRequest) GetResponseMode() oidc.ResponseMode {
	return oidc.ResponseMode(r.ResponseMode)
}

func (r authRequest) GetCodeChallenge() *oidc.CodeChallenge {
	if r.CodeChallenge == "" {
		return nil
	}
	return &oidc.CodeChallenge{
		Challenge: r.CodeChallenge,
		Method:    oidc.CodeChallengeMethod(r.CodeChallengeMethod),
	}
}

// refreshGrant is a stored refresh token as the provider reads it.
type refreshGrant struct {
	Refresh
}

func (*refreshGrant) GetAMR() []string         { return []string{"pwd"} }
func (g *refreshGrant) GetAudience() []string  { return g.Audience }
func (g *refreshGrant) GetAuthTime() time.Time { return g.AuthTime }
func (g *refreshGrant) GetClientID() string    { return g.ClientID }
func (g *refreshGrant) GetScopes() []string    { return g.Scopes }
func (g *refreshGrant) GetSubject() string     { return g.UserID }

// SetCurrentScopes narrows the grant to what a refresh asked for.
func (g *refreshGrant) SetCurrentScopes(scopes []string) { g.Scopes = scopes }

// client is a registered Client as the provider reads it.
type client struct {
	login func(requestID string) string
	Client
}

func (c client) GetID() string                      { return c.ID }
func (c client) RedirectURIs() []string             { return c.Client.RedirectURIs }
func (client) PostLogoutRedirectURIs() []string     { return nil }
func (client) ApplicationType() op.ApplicationType  { return op.ApplicationTypeNative }
func (client) AuthMethod() oidc.AuthMethod          { return oidc.AuthMethodNone }
func (client) AccessTokenType() op.AccessTokenType  { return op.AccessTokenTypeJWT }
func (client) IDTokenLifetime() time.Duration       { return AccessTokenLifetime }
func (client) DevMode() bool                        { return false }
func (client) IDTokenUserinfoClaimsAssertion() bool { return false }
func (client) ClockSkew() time.Duration             { return 0 }
func (c client) LoginURL(requestID string) string   { return c.login(requestID) }
func (client) IsScopeAllowed(scope string) bool     { return scope == Scope }

func (client) ResponseTypes() []oidc.ResponseType { return []oidc.ResponseType{oidc.ResponseTypeCode} }

func (client) GrantTypes() []oidc.GrantType {
	return []oidc.GrantType{oidc.GrantTypeCode, oidc.GrantTypeRefreshToken}
}

//nolint:staticcheck // op.Client fixes this name.
func (client) RestrictAdditionalIdTokenScopes() func([]string) []string {
	return func(scopes []string) []string { return scopes }
}

func (client) RestrictAdditionalAccessTokenScopes() func([]string) []string {
	return func(scopes []string) []string { return scopes }
}

// signingKey is the private half the provider signs with.
type signingKey struct {
	key *ecdsa.PrivateKey
	id  string
}

func (signingKey) SignatureAlgorithm() jose.SignatureAlgorithm { return jose.ES256 }
func (k signingKey) Key() any                                  { return k.key }
func (k signingKey) ID() string                                { return k.id }

// publicKey is the half the key set publishes.
type publicKey struct {
	key *ecdsa.PublicKey
	id  string
}

func (k publicKey) ID() string                       { return k.id }
func (publicKey) Algorithm() jose.SignatureAlgorithm { return jose.ES256 }
func (publicKey) Use() string                        { return "sig" }
func (k publicKey) Key() any                         { return k.key }
