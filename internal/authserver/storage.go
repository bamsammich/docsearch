package authserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// errNoSecrets answers a client presenting a secret: none was ever issued.
var errNoSecrets = errors.New("docsearch's clients are public and hold no secret")

// storage is the provider's op.Storage over a Store.
type storage struct {
	store    Store
	accounts Accounts
	now      func() time.Time
	login    func(requestID string) string
	keys     *keyring
	audience []string
}

// -- authorization requests --------------------------------------------

// CreateAuthRequest records a request as it arrives. offline_access is
// added because the provider issues refresh tokens only for it, and an MCP
// client that had to sign in again every hour would be no use.
//
//nolint:ireturn // op.Storage fixes this signature.
func (s *storage) CreateAuthRequest(
	ctx context.Context,
	req *oidc.AuthRequest,
	_ string,
) (op.AuthRequest, error) {
	scopes := slices.Clone([]string(req.Scopes))
	if !slices.Contains(scopes, oidc.ScopeOfflineAccess) {
		scopes = append(scopes, oidc.ScopeOfflineAccess)
	}
	request := Request{
		CreatedAt:           s.now(),
		ID:                  rand.Text(),
		ClientID:            req.ClientID,
		RedirectURI:         req.RedirectURI,
		State:               req.State,
		Nonce:               req.Nonce,
		ResponseType:        string(req.ResponseType),
		ResponseMode:        string(req.ResponseMode),
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: string(req.CodeChallengeMethod),
		Scopes:              scopes,
		Audience:            s.audience,
	}
	if err := s.store.SaveRequest(ctx, request); err != nil {
		return nil, err
	}
	return authRequest{request}, nil
}

//nolint:ireturn // op.Storage fixes this signature.
func (s *storage) AuthRequestByID(ctx context.Context, id string) (op.AuthRequest, error) {
	request, err := s.current(ctx, id)
	if err != nil {
		return nil, err
	}
	return authRequest{request}, nil
}

// current is a request still young enough to finish.
func (s *storage) current(ctx context.Context, id string) (Request, error) {
	request, found, err := s.store.Request(ctx, id)
	if err != nil {
		return Request{}, err
	}
	if !found {
		return Request{}, errNotFound
	}
	if s.now().Sub(request.CreatedAt) > RequestLifetime {
		return Request{}, ErrExpired
	}
	return request, nil
}

//nolint:ireturn // op.Storage fixes this signature.
func (s *storage) AuthRequestByCode(ctx context.Context, code string) (op.AuthRequest, error) {
	request, found, err := s.store.RequestByCode(ctx, hashOf(code))
	if err != nil {
		return nil, err
	}
	if !found || s.now().Sub(request.CreatedAt) > RequestLifetime {
		return nil, errNotFound
	}
	return authRequest{request}, nil
}

func (s *storage) SaveAuthCode(ctx context.Context, id, code string) error {
	return s.store.SaveCode(ctx, id, hashOf(code))
}

func (s *storage) DeleteAuthRequest(ctx context.Context, id string) error {
	return s.store.DeleteRequest(ctx, id)
}

// -- tokens ---------------------------------------------------------------

func (s *storage) CreateAccessToken(context.Context, op.TokenRequest) (string, time.Time, error) {
	return rand.Text(), s.now().Add(AccessTokenLifetime), nil
}

// CreateAccessAndRefreshTokens issues a refresh token, spending the one it
// replaces so a refresh token works exactly once.
//
//nolint:revive // op.Storage fixes the four results.
func (s *storage) CreateAccessAndRefreshTokens(
	ctx context.Context,
	request op.TokenRequest,
	current string,
) (string, string, time.Time, error) {
	if current != "" {
		if _, found, err := s.store.TakeRefresh(ctx, hashOf(current)); err != nil || !found {
			return "", "", time.Time{}, errors.Join(err, errIf(!found, op.ErrInvalidRefreshToken))
		}
	}
	grant, err := grantOf(request)
	if err != nil {
		return "", "", time.Time{}, err
	}
	token := rand.Text()
	grant.TokenHash = hashOf(token)
	grant.ExpiresAt = s.now().Add(RefreshTokenLifetime)
	if err := s.store.SaveRefresh(ctx, grant); err != nil {
		return "", "", time.Time{}, err
	}
	return rand.Text(), token, s.now().Add(AccessTokenLifetime), nil
}

// grantOf is what a new refresh token carries, from whichever request
// earned it.
func grantOf(request op.TokenRequest) (Refresh, error) {
	switch r := request.(type) {
	case authRequest:
		return Refresh{
			AuthTime: r.AuthTime, ClientID: r.ClientID, UserID: r.UserID,
			Scopes: r.Scopes, Audience: r.Audience,
		}, nil
	case *refreshGrant:
		return Refresh{
			AuthTime: r.AuthTime, ClientID: r.ClientID, UserID: r.UserID,
			Scopes: r.Scopes, Audience: r.Audience,
		}, nil
	default:
		return Refresh{}, fmt.Errorf("no refresh token for a %T", request)
	}
}

// TokenRequestByRefreshToken reads a refresh token, refusing one that has
// expired or whose account is gone.
//
//nolint:ireturn // op.Storage fixes this signature.
func (s *storage) TokenRequestByRefreshToken(
	ctx context.Context,
	token string,
) (op.RefreshTokenRequest, error) {
	refresh, found, err := s.store.Refresh(ctx, hashOf(token))
	if err != nil {
		return nil, err
	}
	if !found || s.now().After(refresh.ExpiresAt) {
		return nil, op.ErrInvalidRefreshToken
	}
	present, err := s.accounts.Exists(ctx, refresh.UserID)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, op.ErrInvalidRefreshToken
	}
	return &refreshGrant{refresh}, nil
}

func (s *storage) TerminateSession(ctx context.Context, userID, clientID string) error {
	return s.store.DeleteRefreshOf(ctx, userID, clientID)
}

// RevokeToken removes a refresh token. An access token cannot be revoked:
// it is a signed JWT nobody looks up, and it expires within the hour.
func (s *storage) RevokeToken(ctx context.Context, tokenOrID, _, _ string) *oidc.Error {
	if err := s.store.DeleteRefresh(ctx, tokenOrID); err != nil {
		return oidc.ErrServerError().WithParent(err)
	}
	return nil
}

// GetRefreshTokenInfo names a refresh token's user and the hash it is
// stored under, which RevokeToken then deletes.
func (s *storage) GetRefreshTokenInfo(
	ctx context.Context,
	clientID, token string,
) (string, string, error) {
	refresh, found, err := s.store.Refresh(ctx, hashOf(token))
	if err != nil {
		return "", "", err
	}
	if !found || refresh.ClientID != clientID {
		return "", "", op.ErrInvalidRefreshToken
	}
	return refresh.UserID, refresh.TokenHash, nil
}

// -- keys -----------------------------------------------------------------

//nolint:ireturn // op.Storage fixes this signature.
func (s *storage) SigningKey(ctx context.Context) (op.SigningKey, error) {
	keys, err := s.keys.load(ctx)
	if err != nil {
		return nil, err
	}
	return signingKey{key: keys[0].key, id: keys[0].id}, nil
}

func (*storage) SignatureAlgorithms(context.Context) ([]jose.SignatureAlgorithm, error) {
	return []jose.SignatureAlgorithm{jose.ES256}, nil
}

func (s *storage) KeySet(ctx context.Context) ([]op.Key, error) {
	keys, err := s.keys.load(ctx)
	if err != nil {
		return nil, err
	}
	set := make([]op.Key, 0, len(keys))
	for _, k := range keys {
		set = append(set, publicKey{key: &k.key.PublicKey, id: k.id})
	}
	return set, nil
}

// -- clients and claims -------------------------------------------------

//nolint:ireturn // op.Storage fixes this signature.
func (s *storage) GetClientByClientID(ctx context.Context, id string) (op.Client, error) {
	registered, found, err := s.store.Client(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errNotFound
	}
	return client{Client: registered, login: s.login}, nil
}

func (*storage) AuthorizeClientIDSecret(context.Context, string, string) error {
	return errNoSecrets
}

func (*storage) SetUserinfoFromScopes(
	_ context.Context,
	info *oidc.UserInfo,
	userID, _ string,
	_ []string,
) error {
	info.Subject = userID
	return nil
}

func (*storage) SetUserinfoFromToken(
	_ context.Context,
	info *oidc.UserInfo,
	_, subject, _ string,
) error {
	info.Subject = subject
	return nil
}

func (*storage) SetIntrospectionFromToken(
	_ context.Context,
	answer *oidc.IntrospectionResponse,
	_, subject, clientID string,
) error {
	answer.Active = true
	answer.Subject = subject
	answer.ClientID = clientID
	return nil
}

func (*storage) GetPrivateClaimsFromScopes(
	context.Context,
	string,
	string,
	[]string,
) (map[string]any, error) {
	return nil, nil //nolint:nilnil // no private claims, which is not a failure
}

func (*storage) GetKeyByIDAndClientID(context.Context, string, string) (*jose.JSONWebKey, error) {
	return nil, errNotFound
}

func (*storage) ValidateJWTProfileScopes(context.Context, string, []string) ([]string, error) {
	return nil, errNoSecrets
}

func (*storage) Health(context.Context) error { return nil }

// errIf is err when cond holds.
func errIf(cond bool, err error) error {
	if cond {
		return err
	}
	return nil
}

// keyring loads the signing keys once, making the first if there is none.
type keyring struct {
	store  Store
	loaded []parsedKey
	mu     sync.Mutex
}

type parsedKey struct {
	key *ecdsa.PrivateKey
	id  string
}

// load is every key, oldest first, creating one on a database that has
// none. Two servers starting at once may each create one; both keep the
// oldest as the one that signs, so their tokens agree.
func (k *keyring) load(ctx context.Context) ([]parsedKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.loaded) > 0 {
		return k.loaded, nil
	}
	stored, err := k.store.Keys(ctx)
	if err != nil {
		return nil, err
	}
	if len(stored) == 0 {
		if stored, err = k.create(ctx); err != nil {
			return nil, err
		}
	}
	parsed, err := parseKeys(stored)
	if err != nil {
		return nil, err
	}
	k.loaded = parsed
	return parsed, nil
}

// parseKeys reads stored keys back into ECDSA keys.
func parseKeys(stored []Key) ([]parsedKey, error) {
	parsed := make([]parsedKey, 0, len(stored))
	for _, key := range stored {
		private, err := x509.ParsePKCS8PrivateKey(key.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("signing key %s: %w", key.ID, err)
		}
		ecKey, ok := private.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("signing key %s is not an ECDSA key", key.ID)
		}
		parsed = append(parsed, parsedKey{key: ecKey, id: key.ID})
	}
	return parsed, nil
}

func (k *keyring) create(ctx context.Context) ([]Key, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, fmt.Errorf("encode signing key: %w", err)
	}
	if err := k.store.AddKey(ctx, Key{ID: rand.Text(), PrivateKey: der}); err != nil {
		return nil, err
	}
	return k.store.Keys(ctx)
}
