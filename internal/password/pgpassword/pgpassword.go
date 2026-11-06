// Package pgpassword keeps password accounts in Postgres.
//
// The sign-in page reads through the request role, and updates the failure
// count, the lock and a rehashed password. Adding, resetting and removing an
// account goes through definer functions, which refuse anyone but a
// docsearch administrator or the owner role, the credential `docsearch
// account` connects with.
package pgpassword

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bamsammich/docsearch/internal/owner"
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

// uniqueViolation is the SQLSTATE of a username another account holds,
// whether in password_accounts or in users.
const uniqueViolation = "23505"

// stranded is the SQLSTATE of removing a shared library's only admin.
const stranded = "DSA04"

// Add creates the account's library and its sign-in in one transaction, so
// there is never a library nobody can sign in to, nor a sign-in with no
// library.
func (s *Store) Add(ctx context.Context, username, userID, hash string, builtin bool) error {
	return s.administer(ctx, func(q *pgdbgen.Queries) error {
		if err := library(ctx, q, userID, builtin); err != nil {
			return err
		}
		err := q.AddPasswordAccount(ctx, pgdbgen.AddPasswordAccountParams{
			Username: username, UserID: userID, PasswordHash: hash,
		})
		if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) &&
			pgErr.Code == uniqueViolation {
			return password.ErrTaken
		}
		if err != nil {
			return fmt.Errorf("add account %s: %w", username, err)
		}
		return nil
	})
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

// Begin counts a sign-in attempt against username, reporting false for an
// account that is locked.
func (s *Store) Begin(
	ctx context.Context,
	username string,
	attempt password.Attempt,
) (bool, error) {
	_, err := s.q.BeginSignIn(ctx, pgdbgen.BeginSignInParams{
		Threshold: pgsession.Narrow(attempt.Threshold),
		LockUntil: attempt.LockUntil,
		Username:  username,
		Now:       attempt.At,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("count sign-in attempt: %w", err)
	}
	return true, nil
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

// Reset replaces a password, lifts any lock, and ends every sign-in the
// account holds.
func (s *Store) Reset(ctx context.Context, username, hash string) error {
	return s.administer(ctx, func(q *pgdbgen.Queries) error {
		found, err := q.ResetPassword(ctx, pgdbgen.ResetPasswordParams{
			Username: username, PasswordHash: hash,
		})
		if err != nil {
			return fmt.Errorf("reset password: %w", err)
		}
		if !found {
			return password.ErrNoSuchAccount
		}
		return nil
	})
}

// Remove ends the account's sign-in and every refresh token it holds. A
// shared library only it administered passes on as handover says.
func (s *Store) Remove(ctx context.Context, username string, handover password.Handover) error {
	return s.administer(ctx, func(q *pgdbgen.Queries) error {
		if err := q.NameHeir(ctx, handover.To); err != nil {
			return fmt.Errorf("remove account: %w", err)
		}
		found, err := q.RemovePasswordAccount(ctx, username)
		return removal(found, err)
	})
}

// removal is the error a removal answered with, in the terms the password
// package uses.
func removal(found bool, err error) error {
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == stranded {
		// The database names the libraries, which is what the operator needs.
		return password.Stranded{Detail: pgErr.Message}
	}
	if err != nil {
		return fmt.Errorf("remove account: %w", err)
	}
	if !found {
		return password.ErrNoSuchAccount
	}
	return nil
}

// administer runs an account change in one transaction, for the
// administrator the context names. The database checks that the user is an
// administrator, and records them in each library's log. A context naming
// nobody is the CLI, which connects as the owner role and needs no user.
func (s *Store) administer(ctx context.Context, work func(*pgdbgen.Queries) error) error {
	if user, err := owner.From(ctx); err == nil {
		return pgsession.Run(ctx, s.db, s.q, pgsession.Personal(user), work)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := work(s.q.WithTx(tx)); err != nil {
		return errors.Join(err, pgsession.Rollback(tx))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
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
