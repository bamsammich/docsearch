// Package pgaccount keeps accounts in Postgres.
//
// The request role cannot write users, so creating and claiming go through
// the SECURITY DEFINER functions migrations 10 and 11 created, and reading
// needs no user because users carries no row-level security.
package pgaccount

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
	"github.com/bamsammich/docsearch/internal/resource"
)

// Store is the account.Store on Postgres.
type Store struct {
	queries *pgdbgen.Queries
}

// New reads and writes accounts through db, as the request role.
func New(db *sql.DB) *Store {
	return &Store{queries: pgdbgen.New(db)}
}

// Find is the account an identity already has.
func (s *Store) Find(ctx context.Context, identity resource.Identity) (string, bool, error) {
	user, err := s.queries.AccountByIdentity(ctx, pgdbgen.AccountByIdentityParams{
		Issuer: identity.Issuer, Subject: identity.Subject,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find account: %w", err)
	}
	return user, true, nil
}

// ClaimBuiltin hands the built-in library to identity, if nobody has yet.
func (s *Store) ClaimBuiltin(
	ctx context.Context,
	identity resource.Identity,
) (string, bool, error) {
	user, err := s.queries.ClaimBuiltin(ctx, pgdbgen.ClaimBuiltinParams{
		Issuer: identity.Issuer, Subject: identity.Subject,
	})
	if err != nil {
		return "", false, fmt.Errorf("claim the built-in library: %w", err)
	}
	return user, user != "", nil
}

// Create makes the account and its partition in one statement.
func (s *Store) Create(ctx context.Context, userID string, identity resource.Identity) error {
	if err := s.queries.CreateAccount(ctx, pgdbgen.CreateAccountParams{
		UserID: userID, Issuer: identity.Issuer, Subject: identity.Subject,
	}); err != nil {
		return fmt.Errorf("create account: %w", err)
	}
	return nil
}
