// Package authserver makes docsearch its own OAuth authorization server, for
// password mode.
//
// A deployment in `oidc` mode lets its provider issue tokens. One with no
// provider still has to give an MCP client what the MCP authorization
// specification asks for: discovery, client registration, the authorization
// code flow with PKCE, and refresh tokens. zitadel/oidc's OpenID Provider does
// the protocol, and is certified for it. What this package adds is the part
// about docsearch: where its state lives, which clients may register, the
// page a person signs in on, and an audience naming docsearch's own `/mcp`
// URI, so the resource server from step 5d verifies these tokens exactly as it
// verifies a provider's.
//
// Every client is public: it registers itself, holds no secret, and must use
// PKCE, which the provider enforces for a public client. Access tokens are
// short-lived JWTs signed with one ES256 key; refresh tokens rotate on every
// use and are stored only as hashes.
package authserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Lifetimes of what the server issues.
const (
	AccessTokenLifetime  = time.Hour
	RefreshTokenLifetime = 30 * 24 * time.Hour
	// RequestLifetime bounds how long a person has to sign in once a client
	// sent them here.
	RequestLifetime = 10 * time.Minute
)

// Scope is the one scope docsearch grants. A client that asks for none is
// given it, since the provider refuses a request with no scope at all.
const Scope = "docsearch"

// errNotFound is a lookup that matched nothing. It satisfies the provider's
// not-found interface, so the provider answers with the right OAuth error.
var errNotFound = notFound{}

type notFound struct{}

func (notFound) Error() string { return "not found" }

func (notFound) IsNotFound() {}

// ErrExpired is a sign-in request older than RequestLifetime.
var ErrExpired = errors.New("this sign-in request has expired; start again from your app")

// Client is a registered client. Every one is public and native in the
// sense of RFC 8252: it holds no secret, uses PKCE, and may redirect to a
// loopback address on any port.
type Client struct {
	CreatedAt    time.Time
	ID           string
	Name         string
	RedirectURIs []string
}

// Request is an authorization request, from the client's redirect to the code
// exchange.
type Request struct {
	CreatedAt           time.Time
	AuthTime            time.Time
	ID                  string
	ClientID            string
	RedirectURI         string
	State               string
	Nonce               string
	ResponseType        string
	ResponseMode        string
	CodeChallenge       string
	CodeChallengeMethod string
	// UserID is set once a person signs in.
	UserID string
	Scopes []string
	// Audience is docsearch's resource URI, fixed when the request arrives.
	Audience []string
}

// Refresh is a refresh token, stored by the hash of what was issued.
type Refresh struct {
	AuthTime  time.Time
	ExpiresAt time.Time
	TokenHash string
	ClientID  string
	UserID    string
	Scopes    []string
	Audience  []string
}

// Key is a signing key, as PKCS #8 DER.
type Key struct {
	ID         string
	PrivateKey []byte
}

// Store is where the server keeps its state.
type Store interface {
	RegisterClient(ctx context.Context, client Client) error
	Client(ctx context.Context, id string) (Client, bool, error)

	SaveRequest(ctx context.Context, request Request) error
	Request(ctx context.Context, id string) (Request, bool, error)
	RequestByCode(ctx context.Context, codeHash string) (Request, bool, error)
	SaveCode(ctx context.Context, id, codeHash string) error
	CompleteRequest(ctx context.Context, id, userID string, at time.Time) error
	DeleteRequest(ctx context.Context, id string) error

	SaveRefresh(ctx context.Context, refresh Refresh) error
	Refresh(ctx context.Context, tokenHash string) (Refresh, bool, error)
	// RotateRefresh spends the refresh token tokenHash names and saves next
	// in its place, together: a token is never spent without its replacement
	// saved, and two exchanges of one token cannot both win. It reports false
	// for a token already spent.
	RotateRefresh(ctx context.Context, tokenHash string, next Refresh) (bool, error)
	DeleteRefresh(ctx context.Context, tokenHash string) error
	DeleteRefreshOf(ctx context.Context, userID, clientID string) error

	// Keys are every signing key, oldest first. The oldest signs.
	Keys(ctx context.Context) ([]Key, error)
	AddKey(ctx context.Context, key Key) error
}

// Accounts is who may sign in.
type Accounts interface {
	Authenticate(ctx context.Context, username, password string) (string, error)
	// Exists reports whether a user still has an account, so removing one
	// stops its tokens at once rather than when they expire.
	Exists(ctx context.Context, userID string) (bool, error)
}

// hashOf is what the store holds in place of a code or a refresh token.
func hashOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
