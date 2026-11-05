package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
	"github.com/bamsammich/docsearch/internal/service/admin"
)

// Admins is the accounts table as one user reaches it.
//
// The reads are plain queries, because the request role reads users. The
// two changes go through definer functions that refuse anyone who is not an
// administrator, so this type holds no rule of its own beyond translating
// the database's refusals.
type Admins struct {
	db   *sql.DB
	q    *pgdbgen.Queries
	user string
}

// NewAdmins reaches the accounts table through db as user.
func NewAdmins(db *sql.DB, user string) *Admins {
	return &Admins{db: db, q: pgdbgen.New(db), user: user}
}

// Me is the user's own username, and whether they administer.
func (a *Admins) Me(ctx context.Context) (admin.Me, error) {
	var me admin.Me
	err := a.run(ctx, func(q *pgdbgen.Queries) error {
		var err error
		if me.Username, err = q.OwnUsername(ctx); err != nil {
			return err
		}
		me.Admin, err = q.IsAdmin(ctx)
		return err
	})
	return me, err
}

// List is every account but the built-in user nobody signs in as.
func (a *Admins) List(ctx context.Context) ([]admin.Account, error) {
	var out []admin.Account
	err := a.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.AccountList(ctx)
		for _, row := range rows {
			out = append(out, admin.Account{
				CreatedAt: row.CreatedAt,
				Username:  row.Username,
				Admin:     row.Admin,
			})
		}
		return err
	})
	return out, err
}

// SetAdmin makes username an administrator, or stops them being one.
func (a *Admins) SetAdmin(ctx context.Context, username string, grant bool) error {
	return a.run(ctx, func(q *pgdbgen.Queries) error {
		return q.SetAdmin(ctx, pgdbgen.SetAdminParams{Username: username, Make: grant})
	})
}

// Record logs a change the user made to username's password account.
func (a *Admins) Record(ctx context.Context, action, username string) error {
	return a.run(ctx, func(q *pgdbgen.Queries) error {
		return q.RecordAccountEvent(ctx, pgdbgen.RecordAccountEventParams{
			Action: action, Username: username,
		})
	})
}

func (a *Admins) run(ctx context.Context, call func(*pgdbgen.Queries) error) error {
	err := pgsession.Run(ctx, a.db, a.q, pgsession.Personal(a.user), call)
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if refusal, ok := adminRefusals[pgErr.Code]; ok {
			return refusal
		}
	}
	return fmt.Errorf("administer accounts: %w", err)
}

// adminRefusals are the admin errors the database's refusals mean, by
// SQLSTATE.
var adminRefusals = map[string]error{
	"DSA01": admin.ErrNotAdmin,
	"DSA02": admin.ErrNoAccount,
	"DSA03": admin.ErrLastAdmin,
}

// GrantAdmin makes username an administrator, or stops them being one, as
// the database owner: the root of trust that makes the first administrator.
// It reports false for a username no account holds.
func GrantAdmin(ctx context.Context, owner *sql.DB, username string, grant bool) (bool, error) {
	rows, err := pgdbgen.New(owner).GrantAdminAsOwner(ctx, pgdbgen.GrantAdminAsOwnerParams{
		Username: username, Make: grant,
	})
	if err != nil {
		return false, fmt.Errorf("set administrator: %w", err)
	}
	return rows > 0, nil
}
