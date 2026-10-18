// Package pgauth keeps the authorization server's state in Postgres, so any
// number of servers sharing one database agree on clients, sign-ins in
// flight, refresh tokens and the signing key.
package pgauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/authserver"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
)

// pruneAfter is how old an abandoned sign-in request gets before the next
// one clears it: well past authserver.RequestLifetime, so nothing a person
// could still finish is removed.
const pruneAfter = time.Hour

// pruneClientsAfter is how long a registered client has to sign someone in
// before the next registration clears it.
const pruneClientsAfter = 24 * time.Hour

// Store is authserver.Store on Postgres.
type Store struct {
	db *sql.DB
	q  *pgdbgen.Queries
}

// New keeps the server's state through db, as the request role.
func New(db *sql.DB) *Store {
	return &Store{db: db, q: pgdbgen.New(db)}
}

func (s *Store) RegisterClient(ctx context.Context, client authserver.Client) error {
	if err := s.q.PruneOAuthClients(ctx, client.CreatedAt.Add(-pruneClientsAfter)); err != nil {
		return fmt.Errorf("prune clients: %w", err)
	}
	uris, err := json.Marshal(client.RedirectURIs)
	if err != nil {
		return err
	}
	if err := s.q.InsertOAuthClient(ctx, pgdbgen.InsertOAuthClientParams{
		ClientID:     client.ID,
		ClientName:   client.Name,
		RedirectUris: uris,
		CreatedAt:    client.CreatedAt,
	}); err != nil {
		return fmt.Errorf("register client: %w", err)
	}
	return nil
}

func (s *Store) Client(ctx context.Context, id string) (authserver.Client, bool, error) {
	row, err := s.q.OAuthClient(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return authserver.Client{}, false, nil
	}
	if err != nil {
		return authserver.Client{}, false, fmt.Errorf("read client: %w", err)
	}
	client := authserver.Client{ID: row.ClientID, Name: row.ClientName, CreatedAt: row.CreatedAt}
	if err := json.Unmarshal(row.RedirectUris, &client.RedirectURIs); err != nil {
		return authserver.Client{}, false, fmt.Errorf("read client %s: %w", id, err)
	}
	return client, true, nil
}

func (s *Store) SaveRequest(ctx context.Context, request authserver.Request) error {
	if err := s.q.PruneOAuthRequests(ctx, request.CreatedAt.Add(-pruneAfter)); err != nil {
		return fmt.Errorf("prune sign-in requests: %w", err)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if err := s.q.InsertOAuthRequest(ctx, pgdbgen.InsertOAuthRequestParams{
		ID: request.ID, ClientID: request.ClientID, Request: body, CreatedAt: request.CreatedAt,
	}); err != nil {
		return fmt.Errorf("save sign-in request: %w", err)
	}
	return nil
}

func (s *Store) Request(ctx context.Context, id string) (authserver.Request, bool, error) {
	row, err := s.q.OAuthRequest(ctx, id)
	return requestOf(row.Request, row.UserID, row.AuthTime, err)
}

func (s *Store) RequestByCode(
	ctx context.Context,
	codeHash string,
) (authserver.Request, bool, error) {
	row, err := s.q.OAuthRequestByCode(ctx, sql.NullString{String: codeHash, Valid: true})
	return requestOf(row.Request, row.UserID, row.AuthTime, err)
}

// requestOf rebuilds a request from its stored JSON and the columns set when
// a person signed in.
func requestOf(
	body json.RawMessage,
	userID sql.NullString,
	authTime sql.NullTime,
	err error,
) (authserver.Request, bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return authserver.Request{}, false, nil
	}
	if err != nil {
		return authserver.Request{}, false, fmt.Errorf("read sign-in request: %w", err)
	}
	var request authserver.Request
	if err := json.Unmarshal(body, &request); err != nil {
		return authserver.Request{}, false, fmt.Errorf("read sign-in request: %w", err)
	}
	request.UserID = userID.String
	if authTime.Valid {
		request.AuthTime = authTime.Time
	}
	return request, true, nil
}

func (s *Store) SaveCode(ctx context.Context, id, codeHash string) error {
	return wrap("save code", s.q.SetOAuthCode(ctx, pgdbgen.SetOAuthCodeParams{
		ID: id, CodeHash: sql.NullString{String: codeHash, Valid: true},
	}))
}

// CompleteRequest records who signed in, and marks the request's client as
// one someone uses, in one transaction.
func (s *Store) CompleteRequest(ctx context.Context, id, userID string, at time.Time) error {
	return s.inTx(ctx, "complete sign-in", func(q *pgdbgen.Queries) error {
		if err := q.CompleteOAuthRequest(ctx, pgdbgen.CompleteOAuthRequestParams{
			ID: id, UserID: sql.NullString{String: userID, Valid: true},
			AuthTime: sql.NullTime{Time: at, Valid: true},
		}); err != nil {
			return err
		}
		return q.MarkOAuthClientSignedIn(ctx, pgdbgen.MarkOAuthClientSignedInParams{
			At: at, RequestID: id,
		})
	})
}

func (s *Store) DeleteRequest(ctx context.Context, id string) error {
	return wrap("delete sign-in request", s.q.DeleteOAuthRequest(ctx, id))
}

func (s *Store) SaveRefresh(ctx context.Context, refresh authserver.Refresh) error {
	return wrap("save refresh token", saveRefresh(ctx, s.q, refresh))
}

func saveRefresh(ctx context.Context, q *pgdbgen.Queries, refresh authserver.Refresh) error {
	scopes, err := json.Marshal(refresh.Scopes)
	if err != nil {
		return err
	}
	audience, err := json.Marshal(refresh.Audience)
	if err != nil {
		return err
	}
	return q.InsertRefreshToken(ctx, pgdbgen.InsertRefreshTokenParams{
		TokenHash: refresh.TokenHash,
		ClientID:  refresh.ClientID,
		UserID:    refresh.UserID,
		Scopes:    scopes,
		Audience:  audience,
		AuthTime:  refresh.AuthTime,
		ExpiresAt: refresh.ExpiresAt,
	})
}

func (s *Store) Refresh(ctx context.Context, tokenHash string) (authserver.Refresh, bool, error) {
	return refreshOf(s.q.RefreshToken(ctx, tokenHash))
}

// RotateRefresh spends one refresh token and saves its replacement in one
// transaction, reporting false for a token that was already spent.
func (s *Store) RotateRefresh(
	ctx context.Context,
	tokenHash string,
	next authserver.Refresh,
) (bool, error) {
	found := false
	err := s.inTx(ctx, "rotate refresh token", func(q *pgdbgen.Queries) error {
		_, err := q.TakeRefreshToken(ctx, tokenHash)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return saveRefresh(ctx, q, next)
	})
	return found, err
}

// inTx runs write in one transaction, naming what failed as doing.
func (s *Store) inTx(ctx context.Context, doing string, write func(*pgdbgen.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}
	if err := write(s.q.WithTx(tx)); err != nil {
		return errors.Join(fmt.Errorf("%s: %w", doing, err), tx.Rollback())
	}
	return wrap(doing, tx.Commit())
}

func refreshOf(row pgdbgen.OauthRefreshToken, err error) (authserver.Refresh, bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return authserver.Refresh{}, false, nil
	}
	if err != nil {
		return authserver.Refresh{}, false, fmt.Errorf("read refresh token: %w", err)
	}
	refresh := authserver.Refresh{
		AuthTime: row.AuthTime, ExpiresAt: row.ExpiresAt, TokenHash: row.TokenHash,
		ClientID: row.ClientID, UserID: row.UserID,
	}
	if err := errors.Join(json.Unmarshal(row.Scopes, &refresh.Scopes),
		json.Unmarshal(row.Audience, &refresh.Audience)); err != nil {
		return authserver.Refresh{}, false, fmt.Errorf("read refresh token: %w", err)
	}
	return refresh, true, nil
}

func (s *Store) DeleteRefresh(ctx context.Context, tokenHash string) error {
	return wrap("delete refresh token", s.q.DeleteRefreshToken(ctx, tokenHash))
}

func (s *Store) DeleteRefreshOf(ctx context.Context, userID, clientID string) error {
	return wrap("end sign-ins", s.q.DeleteRefreshTokensOf(ctx, pgdbgen.DeleteRefreshTokensOfParams{
		UserID: userID, ClientID: clientID,
	}))
}

func (s *Store) Keys(ctx context.Context) ([]authserver.Key, error) {
	rows, err := s.q.OAuthKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("read signing keys: %w", err)
	}
	keys := make([]authserver.Key, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, authserver.Key{ID: row.KeyID, PrivateKey: row.PrivateKey})
	}
	return keys, nil
}

func (s *Store) AddKey(ctx context.Context, key authserver.Key) error {
	return wrap("add signing key", s.q.InsertOAuthKey(ctx, pgdbgen.InsertOAuthKeyParams{
		KeyID: key.ID, PrivateKey: key.PrivateKey,
	}))
}

func wrap(what string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
