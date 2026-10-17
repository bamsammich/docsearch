// Package pgpassword keeps password accounts in Postgres.
//
// The sign-in page reads through the request role. Adding and removing
// accounts runs as whoever holds the owner's DSN, the same credential that
// migrates the schema, so administering accounts needs no role system inside
// docsearch: the operator is whoever can reach the database as its owner.
package pgpassword

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
)

// Store is password.Store on Postgres.
type Store struct {
	db *sql.DB
	q  *pgdbgen.Queries
}

// New keeps accounts through db.
func New(db *sql.DB) *Store {
	return &Store{db: db, q: pgdbgen.New(db)}
}

// Add creates the account's library and its sign-in in one transaction, so
// there is never a library nobody can sign in to, nor a sign-in with no
// library.
func (s *Store) Add(ctx context.Context, username, userID, hash string, builtin bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("add account: %w", err)
	}
	q := s.q.WithTx(tx)
	if err := library(ctx, q, userID, builtin); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := q.InsertPasswordAccount(ctx, pgdbgen.InsertPasswordAccountParams{
		Username: username, UserID: userID, PasswordHash: hash,
	}); err != nil {
		return errors.Join(fmt.Errorf("add account %s: %w", username, err), tx.Rollback())
	}
	return tx.Commit()
}

// library is the account's library: the built-in one, claimed, or a new one.
func library(ctx context.Context, q *pgdbgen.Queries, userID string, builtin bool) error {
	if !builtin {
		if err := q.CreatePasswordUser(ctx, userID); err != nil {
			return fmt.Errorf("create library: %w", err)
		}
		return nil
	}
	claimed, err := q.ClaimBuiltinForPassword(ctx)
	if err != nil {
		return fmt.Errorf("claim the built-in library: %w", err)
	}
	if claimed == "" {
		return password.ErrBuiltinTaken
	}
	return nil
}

// Find is the account signing in under username.
func (s *Store) Find(ctx context.Context, username string) (password.Record, bool, error) {
	row, err := s.q.PasswordAccountByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return password.Record{}, false, nil
	}
	if err != nil {
		return password.Record{}, false, fmt.Errorf("find account: %w", err)
	}
	return password.Record{
		Hash: row.PasswordHash,
		Account: password.Account{
			CreatedAt:   row.CreatedAt,
			LockedUntil: timeOf(row.LockedUntil),
			Username:    row.Username,
			UserID:      row.UserID,
			Failures:    int(row.FailedAttempts),
		},
	}, true, nil
}

// Failed counts a failure, locking the account once it reaches threshold.
func (s *Store) Failed(
	ctx context.Context,
	username string,
	threshold int,
	lockUntil time.Time,
) error {
	if err := s.q.RecordFailedSignIn(ctx, pgdbgen.RecordFailedSignInParams{
		Threshold: pgsession.Narrow(threshold), LockUntil: lockUntil, Username: username,
	}); err != nil {
		return fmt.Errorf("record failed sign-in: %w", err)
	}
	return nil
}

// Succeeded clears the failures a correct password ends.
func (s *Store) Succeeded(ctx context.Context, username string) error {
	if err := s.q.ClearFailedSignIns(ctx, username); err != nil {
		return fmt.Errorf("clear failed sign-ins: %w", err)
	}
	return nil
}

// SetHash replaces a password and lifts any lock.
func (s *Store) SetHash(ctx context.Context, username, hash string) error {
	rows, err := s.q.SetPasswordHash(ctx, pgdbgen.SetPasswordHashParams{
		Username: username, PasswordHash: hash,
	})
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	if rows == 0 {
		return password.ErrNoSuchAccount
	}
	return nil
}

// Remove ends the account's sign-in and every refresh token it holds.
func (s *Store) Remove(ctx context.Context, username string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("remove account: %w", err)
	}
	q := s.q.WithTx(tx)
	userID, err := q.DeletePasswordAccount(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.Join(password.ErrNoSuchAccount, tx.Rollback())
	}
	if err != nil {
		return errors.Join(fmt.Errorf("remove account: %w", err), tx.Rollback())
	}
	if err := q.DeleteRefreshTokensOfUser(ctx, userID); err != nil {
		return errors.Join(fmt.Errorf("end sign-ins: %w", err), tx.Rollback())
	}
	return tx.Commit()
}

// List is every account, by username.
func (s *Store) List(ctx context.Context) ([]password.Account, error) {
	rows, err := s.q.ListPasswordAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	accounts := make([]password.Account, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, password.Account{
			CreatedAt:   row.CreatedAt,
			LockedUntil: timeOf(row.LockedUntil),
			Username:    row.Username,
			UserID:      row.UserID,
			Failures:    int(row.FailedAttempts),
		})
	}
	return accounts, nil
}

// Exists reports whether userID still has a password account.
func (s *Store) Exists(ctx context.Context, userID string) (bool, error) {
	present, err := s.q.PasswordAccountExists(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("check account: %w", err)
	}
	return present, nil
}

func timeOf(t sql.NullTime) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}
