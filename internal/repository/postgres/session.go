package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/bamsammich/docsearch/internal/store/pgdbgen"
)

// Nothing in this package touches the database outside a transaction that
// has named its user.
//
// The policies read app.user_id, and SET LOCAL lasts for one transaction, so
// a statement run on a bare connection matches no rows at all. Wrapping even
// a single read costs one round trip and buys a rule with no exceptions: a
// method that forgot the user would return nothing rather than return
// somebody else's rows, and there is no way to forget it without also
// forgetting to open a transaction.
//
// Session-level SET would avoid the round trip, and was rejected: database/sql
// hands out whichever pooled connection is free, so a session variable set
// for one request would still be set when the next request borrowed that
// connection.

// session opens a transaction scoped to userID and runs work in it.
func session(
	ctx context.Context,
	db *sql.DB,
	q *pgdbgen.Queries,
	userID string,
	work func(*pgdbgen.Queries) error,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := setUser(ctx, tx, userID); err != nil {
		return errors.Join(err, rollback(tx))
	}
	if err := work(q.WithTx(tx)); err != nil {
		return errors.Join(err, rollback(tx))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// setUser is what the policies read. SET LOCAL takes no placeholder, so the
// identifier is quoted rather than bound; it comes from the authenticated
// request rather than from a document.
func setUser(ctx context.Context, tx *sql.Tx, userID string) error {
	if userID == "" {
		return errors.New("no user: a statement without an owner would match no rows")
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("SET LOCAL app.user_id = %s", quoteLiteral(userID))); err != nil {
		return fmt.Errorf("set the user: %w", err)
	}
	return nil
}

// quoteLiteral wraps a value as Postgres does, doubling any quote inside it.
func quoteLiteral(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, '\'')
	for _, r := range s {
		if r == '\'' {
			out = append(out, '\'')
		}
		out = append(out, r)
	}
	return string(append(out, '\''))
}

// rollback reports a rollback that itself failed, and says nothing about one
// the driver already performed.
func rollback(tx *sql.Tx) error {
	err := tx.Rollback()
	if err == nil || errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return fmt.Errorf("roll back: %w", err)
}

// read runs one statement in a transaction scoped to userID and answers
// with what the statement produced.
//
// Reads go through session() like writes, so there is one path to the
// database and no rollback whose failure nobody is told about. A read-only
// transaction costs the same round trip either way.
//
//nolint:ireturn // the return is whatever the statement returns; ireturn reads every type parameter as an interface.
func read[T any](
	ctx context.Context,
	db *sql.DB,
	q *pgdbgen.Queries,
	userID string,
	statement func(*pgdbgen.Queries) (T, error),
) (T, error) {
	var out T
	err := session(ctx, db, q, userID, func(q *pgdbgen.Queries) error {
		var readErr error
		out, readErr = statement(q)
		return readErr
	})
	return out, err
}

// narrow fits a Go int into the int32 a Postgres integer column takes.
//
// Clamped rather than wrapped: a progress count or an attempt ceiling past
// two billion is a caller's mistake, and a wrapped negative would read as a
// job that had never been tried.
func narrow(v int) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	}
	return int32(v)
}
