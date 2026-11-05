// Package pgsession is how every Postgres statement in docsearch names the
// user and the library it belongs to.
//
// Nothing outside this package should touch the database except through it.
//
// The policies read app.user_id and app.library_id, and a setting made with
// SET LOCAL lasts for one transaction, so a statement run on a bare
// connection matches no rows at all. Wrapping even
// a single read costs one round trip and buys a rule with no exceptions: a
// method that forgot the user would return nothing rather than return
// somebody else's rows, and there is no way to forget it without also
// forgetting to open a transaction.
//
// Session-level SET would avoid the round trip, and was rejected: database/sql
// hands out whichever pooled connection is free, so a session variable set
// for one request would still be set when the next request borrowed that
// connection.
package pgsession

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
)

// Scope is who a transaction acts for and which library it works in.
//
// The two differ once a library is shared: a member reads and writes a
// library that is not their own, and the policies check that the user holds
// a grant on the library. A user's personal library carries the user's id.
type Scope struct {
	User    string
	Library string
}

// Personal is the scope of a user working in their own library.
func Personal(user string) Scope {
	return Scope{User: user, Library: user}
}

// Run opens a transaction in scope and runs work in it.
func Run(
	ctx context.Context,
	db *sql.DB,
	q *pgdbgen.Queries,
	scope Scope,
	work func(*pgdbgen.Queries) error,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := setScope(ctx, tx, scope); err != nil {
		return errors.Join(err, Rollback(tx))
	}
	if err := work(q.WithTx(tx)); err != nil {
		return errors.Join(err, Rollback(tx))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// setScope is what the policies read: app.user_id is who is asking, and
// app.library_id is the library the transaction works in.
//
// set_config with is_local true is SET LOCAL as a function, which lets both
// settings go in one statement with the identifiers bound rather than quoted.
func setScope(ctx context.Context, tx *sql.Tx, scope Scope) error {
	if scope.User == "" || scope.Library == "" {
		return errors.New("no scope: a statement without a user and a library would match no rows")
	}
	if _, err := tx.ExecContext(ctx,
		"SELECT set_config('app.user_id', $1, true), set_config('app.library_id', $2, true)",
		scope.User, scope.Library); err != nil {
		return fmt.Errorf("set the scope: %w", err)
	}
	return nil
}

// Rollback reports a rollback that itself failed, and says nothing about one
// the driver already performed.
func Rollback(tx *sql.Tx) error {
	err := tx.Rollback()
	if err == nil || errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return fmt.Errorf("roll back: %w", err)
}

// Read runs one statement in a transaction in scope and answers with
// what the statement produced.
//
// A read opens a transaction exactly as a write does, so there is one path to
// the database and no rollback whose failure nobody is told about. The round
// trip costs the same either way.
//
//nolint:ireturn // the return is whatever the statement returns; ireturn reads every type parameter as an interface.
func Read[T any](
	ctx context.Context,
	db *sql.DB,
	q *pgdbgen.Queries,
	scope Scope,
	statement func(*pgdbgen.Queries) (T, error),
) (T, error) {
	var out T
	err := Run(ctx, db, q, scope, func(q *pgdbgen.Queries) error {
		var readErr error
		out, readErr = statement(q)
		return readErr
	})
	return out, err
}

// Narrow fits a Go int into the int32 a Postgres integer column takes.
//
// Clamped rather than wrapped: a progress count or an attempt ceiling past
// two billion is a caller's mistake, and a wrapped negative would read as a
// job that had never been tried.
func Narrow(v int) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	}
	return int32(v)
}

// ReadOnly opens a read-only transaction in scope and runs work in
// it, for a read that asks several questions and wants one round trip to
// begin and one to end rather than one pair per question.
//
// A read commits nothing, so the happy path rolls back and reports a rollback
// that genuinely failed.
func ReadOnly(
	ctx context.Context,
	db *sql.DB,
	scope Scope,
	work func(*sql.Tx) error,
) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := setScope(ctx, tx, scope); err != nil {
		return errors.Join(err, Rollback(tx))
	}
	if err := work(tx); err != nil {
		return errors.Join(err, Rollback(tx))
	}
	return Rollback(tx)
}

// Rows runs one statement whose SQL is assembled rather than generated, and
// hands its rows to scan before the transaction can close: a *sql.Rows from a
// finished transaction yields nothing.
func Rows(
	ctx context.Context,
	tx *sql.Tx,
	statement string,
	args []any,
	scan func(*sql.Rows) error,
) error {
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if err := scan(rows); err != nil {
		return err
	}
	return rows.Err()
}
