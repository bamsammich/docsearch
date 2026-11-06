package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// Libraries creates and shares libraries as one user.
//
// Each call runs in a transaction that names the user, and calls a definer
// function that checks the user's role before it changes anything, so this
// type holds no rule of its own beyond translating the database's refusals.
type Libraries struct {
	db   *sql.DB
	q    *pgdbgen.Queries
	user string
}

// NewLibraries manages libraries through db as user.
func NewLibraries(db *sql.DB, user string) *Libraries {
	return &Libraries{db: db, q: pgdbgen.New(db), user: user}
}

// List is every library the user reads, their own first.
func (l *Libraries) List(ctx context.Context) ([]sharing.Library, error) {
	libs, err := pgstore.New(l.db, pgsession.Personal(l.user)).Libraries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]sharing.Library, 0, len(libs))
	for _, lib := range libs {
		out = append(out, sharing.Library{Name: lib.Label, Title: lib.Name, Role: lib.Role})
	}
	return out, nil
}

// Username is the user's own, empty until a sign-in records it.
func (l *Libraries) Username(ctx context.Context) (string, error) {
	var name string
	err := l.run(ctx, func(q *pgdbgen.Queries) error {
		var err error
		name, err = q.OwnUsername(ctx)
		return err
	})
	return name, err
}

// Create makes a shared library with the user as its admin.
func (l *Libraries) Create(ctx context.Context, slug, name string) (sharing.Library, error) {
	err := l.run(ctx, func(q *pgdbgen.Queries) error {
		_, err := q.CreateLibrary(ctx, pgdbgen.CreateLibraryParams{Slug: slug, Name: name})
		return err
	})
	if err != nil {
		return sharing.Library{}, err
	}
	return sharing.Library{Name: slug, Title: name, Role: sharing.Admin}, nil
}

// Grant changes username's role on the library slug names, or invites them
// with role when they hold none. It reports whether it invited.
func (l *Libraries) Grant(ctx context.Context, slug, username, role string) (bool, error) {
	var outcome string
	err := l.run(ctx, func(q *pgdbgen.Queries) error {
		var err error
		outcome, err = q.GrantLibrary(
			ctx,
			pgdbgen.GrantLibraryParams{Slug: slug, Username: username, Role: role},
		)
		return err
	})
	return outcome == "invited", err
}

// Invitations are the libraries the user is asked to join, newest first.
func (l *Libraries) Invitations(ctx context.Context) ([]sharing.Invitation, error) {
	var out []sharing.Invitation
	err := l.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.MyInvitations(ctx)
		for _, row := range rows {
			out = append(out, sharing.Invitation{
				At:        row.InvitedAt,
				Library:   row.Slug,
				Title:     row.Name,
				Role:      row.Role,
				InvitedBy: row.InvitedBy,
			})
		}
		return err
	})
	return out, err
}

// Accept joins the library slug names with the role its invitation offered.
func (l *Libraries) Accept(ctx context.Context, slug string) (sharing.Library, error) {
	var role string
	err := l.run(ctx, func(q *pgdbgen.Queries) error {
		var err error
		role, err = q.AcceptInvitation(ctx, slug)
		return err
	})
	if err != nil {
		return sharing.Library{}, err
	}
	libs, err := l.List(ctx)
	if err != nil {
		return sharing.Library{}, err
	}
	for _, lib := range libs {
		if lib.Name == slug {
			return lib, nil
		}
	}
	return sharing.Library{Name: slug, Role: role}, nil
}

// Decline refuses the invitation to the library slug names.
func (l *Libraries) Decline(ctx context.Context, slug string) error {
	return l.run(ctx, func(q *pgdbgen.Queries) error {
		return q.DeclineInvitation(ctx, slug)
	})
}

// Revoke takes username's role on the library slug names away.
func (l *Libraries) Revoke(ctx context.Context, slug, username string) error {
	return l.run(ctx, func(q *pgdbgen.Queries) error {
		return q.RevokeLibrary(ctx, pgdbgen.RevokeLibraryParams{Slug: slug, Username: username})
	})
}

// Members is everyone with a role on the library slug names.
func (l *Libraries) Members(ctx context.Context, slug string) ([]sharing.Member, error) {
	var out []sharing.Member
	err := l.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.LibraryMembers(ctx, slug)
		for _, row := range rows {
			out = append(out, sharing.Member{
				Username: row.Username,
				Role:     row.Role,
				Invited:  row.Invited,
			})
		}
		return err
	})
	return out, err
}

// Events is the record of changes to who may use the library slug names.
func (l *Libraries) Events(ctx context.Context, slug string) ([]sharing.Event, error) {
	var out []sharing.Event
	err := l.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.LibraryEvents(ctx, slug)
		for _, row := range rows {
			out = append(out, sharing.Event{
				At:      row.At,
				Actor:   row.Actor,
				Action:  row.Action,
				Subject: row.Subject,
				Role:    row.Role,
			})
		}
		return err
	})
	return out, err
}

// run calls one management function as the user, and names its refusal.
func (l *Libraries) run(ctx context.Context, call func(*pgdbgen.Queries) error) error {
	err := pgsession.Run(ctx, l.db, l.q, pgsession.Personal(l.user), call)
	if err == nil {
		return nil
	}
	if refusal := refusalOf(err); refusal != nil {
		return refusal
	}
	return fmt.Errorf("manage libraries: %w", err)
}

// refusals are the sharing errors the database's refusals mean, by SQLSTATE.
var refusals = map[string]error{
	"DSL01": sharing.ErrNoLibrary,
	"DSL02": sharing.ErrNoAccount,
	"DSL03": sharing.ErrLastAdmin,
	"DSL04": sharing.ErrNameTaken,
	"DSL05": sharing.ErrNoInvitation,
	"23505": sharing.ErrNameTaken,
	"23514": sharing.ErrInvalid,
	"22023": sharing.ErrInvalid,
}

// refusalOf is the sharing error a database refusal means, or nil for an
// error that is not one.
func refusalOf(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	return refusals[pgErr.Code]
}
