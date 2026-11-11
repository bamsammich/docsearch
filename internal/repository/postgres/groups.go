package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// Groups creates and runs groups as one user, and grants libraries to them.
//
// As with Libraries, each call runs in a transaction that names the user and
// calls a definer function that checks the user's place first, so this type
// holds no rule beyond translating the database's refusals.
type Groups struct {
	db   *sql.DB
	q    *pgdbgen.Queries
	user string
}

// NewGroups manages groups through db as user.
func NewGroups(db *sql.DB, user string) *Groups {
	return &Groups{db: db, q: pgdbgen.New(db), user: user}
}

// Create makes a group with the user as its maintainer.
func (g *Groups) Create(ctx context.Context, slug, name string) (sharing.Group, error) {
	err := g.run(ctx, func(q *pgdbgen.Queries) error {
		_, err := q.CreateGroup(ctx, pgdbgen.CreateGroupParams{Slug: slug, Name: name})
		return err
	})
	if err != nil {
		return sharing.Group{}, err
	}
	return sharing.Group{Slug: slug, Name: name, Role: sharing.RoleMaintainer, Members: 1}, nil
}

// Rename changes the name people read the group slug names by.
func (g *Groups) Rename(ctx context.Context, slug, name string) error {
	return g.run(ctx, func(q *pgdbgen.Queries) error {
		return q.RenameGroup(ctx, pgdbgen.RenameGroupParams{Slug: slug, Name: name})
	})
}

// Delete removes the group slug names, and its grants with it.
func (g *Groups) Delete(ctx context.Context, slug string) error {
	return g.run(ctx, func(q *pgdbgen.Queries) error { return q.DeleteGroup(ctx, slug) })
}

// Invite changes the role of a member, or invites anyone else. It reports
// whether it invited.
func (g *Groups) Invite(ctx context.Context, slug, username, role string) (bool, error) {
	var outcome string
	err := g.run(ctx, func(q *pgdbgen.Queries) error {
		var err error
		outcome, err = q.InviteToGroup(ctx, pgdbgen.InviteToGroupParams{
			Slug: slug, Username: username, Role: role,
		})
		return err
	})
	return outcome == "invited", err
}

// Force makes username a member of the group slug names at once, with no
// invitation to accept, or changes their role. Only an administrator may,
// and the group and each of its libraries record it.
func (g *Groups) Force(ctx context.Context, slug, username, role string) error {
	return g.run(ctx, func(q *pgdbgen.Queries) error {
		return q.ForceIntoGroup(ctx, pgdbgen.ForceIntoGroupParams{
			Slug: slug, Username: username, Role: role,
		})
	})
}

// Accept joins the group the user is invited to, and answers the role.
func (g *Groups) Accept(ctx context.Context, slug string) (string, error) {
	var role string
	err := g.run(ctx, func(q *pgdbgen.Queries) error {
		var err error
		role, err = q.AcceptGroupInvitation(ctx, slug)
		return err
	})
	return role, err
}

// Decline refuses the invitation to the group slug names.
func (g *Groups) Decline(ctx context.Context, slug string) error {
	return g.run(ctx, func(q *pgdbgen.Queries) error { return q.DeclineGroupInvitation(ctx, slug) })
}

// Remove takes username out of the group, or withdraws their invitation.
// Removing oneself is leaving.
func (g *Groups) Remove(ctx context.Context, slug, username string) error {
	return g.run(ctx, func(q *pgdbgen.Queries) error {
		return q.RemoveFromGroup(ctx, pgdbgen.RemoveFromGroupParams{Slug: slug, Username: username})
	})
}

// All is every group, with the user's role in each.
func (g *Groups) All(ctx context.Context) ([]sharing.Group, error) {
	var out []sharing.Group
	err := g.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.AllGroups(ctx)
		for _, row := range rows {
			out = append(out, sharing.Group{
				Slug: row.Slug, Name: row.Name, Role: row.Role, Members: row.Members,
			})
		}
		return err
	})
	return out, err
}

// Invitations are the groups the user is asked to join, newest first.
func (g *Groups) Invitations(ctx context.Context) ([]sharing.GroupInvitation, error) {
	var out []sharing.GroupInvitation
	err := g.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.MyGroupInvitations(ctx)
		for _, row := range rows {
			out = append(out, sharing.GroupInvitation{
				At: row.InvitedAt, Group: row.Slug, Name: row.Name,
				Role: row.Role, InvitedBy: row.InvitedBy,
			})
		}
		return err
	})
	return out, err
}

// Members is who is in the group slug names; its maintainers also see who
// is invited.
func (g *Groups) Members(ctx context.Context, slug string) ([]sharing.GroupMember, error) {
	var out []sharing.GroupMember
	err := g.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.GroupMembers(ctx, slug)
		for _, row := range rows {
			out = append(out, sharing.GroupMember{
				Username:     row.Username,
				Role:         row.Role,
				FromProvider: row.Source == "provider",
				Invited:      row.Invited,
			})
		}
		return err
	})
	return out, err
}

// Libraries are the libraries granted to the group slug names.
func (g *Groups) Libraries(ctx context.Context, slug string) ([]sharing.Library, error) {
	var out []sharing.Library
	err := g.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.GroupLibraries(ctx, slug)
		for _, row := range rows {
			out = append(out, sharing.Library{Name: row.Slug, Title: row.Name, Role: row.Role})
		}
		return err
	})
	return out, err
}

// Events is the record of changes to the group slug names.
func (g *Groups) Events(ctx context.Context, slug string) ([]sharing.Event, error) {
	var out []sharing.Event
	err := g.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.GroupEvents(ctx, slug)
		for _, row := range rows {
			out = append(out, sharing.Event{
				At: row.At, Actor: row.Actor, Action: row.Action,
				Subject: row.Subject, Role: row.Role, Detail: row.Detail,
			})
		}
		return err
	})
	return out, err
}

// GrantLibrary gives the group slug names role on library, or changes it.
func (g *Groups) GrantLibrary(ctx context.Context, library, slug, role string) error {
	return g.run(ctx, func(q *pgdbgen.Queries) error {
		return q.GrantLibraryGroup(ctx, pgdbgen.GrantLibraryGroupParams{
			Library: library, Slug: slug, Role: role,
		})
	})
}

// RevokeLibrary takes the group's role on library away.
func (g *Groups) RevokeLibrary(ctx context.Context, library, slug string) error {
	return g.run(ctx, func(q *pgdbgen.Queries) error {
		return q.RevokeLibraryGroup(ctx, pgdbgen.RevokeLibraryGroupParams{
			Library: library, Slug: slug,
		})
	})
}

// OnLibrary are the groups library is granted to.
func (g *Groups) OnLibrary(ctx context.Context, library string) ([]sharing.GroupGrant, error) {
	var out []sharing.GroupGrant
	err := g.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.LibraryGroups(ctx, library)
		for _, row := range rows {
			out = append(out, sharing.GroupGrant{
				Group: row.Slug, Name: row.Name, Role: row.Role, Members: row.Members,
			})
		}
		return err
	})
	return out, err
}

// Link makes the group slug names follow providerGroup, or stops it
// following one when providerGroup is empty. Only an administrator may.
func (g *Groups) Link(ctx context.Context, slug, providerGroup string) error {
	return g.run(ctx, func(q *pgdbgen.Queries) error {
		return q.LinkGroup(ctx, pgdbgen.LinkGroupParams{Slug: slug, ProviderGroup: providerGroup})
	})
}

// ProviderLink is the provider group the group slug names follows, and
// whether the user is an administrator. Anyone else learns no link.
func (g *Groups) ProviderLink(ctx context.Context, slug string) (sharing.ProviderLink, error) {
	var out sharing.ProviderLink
	err := g.run(ctx, func(q *pgdbgen.Queries) error {
		row, err := q.GroupProviderLink(ctx, slug)
		out = sharing.ProviderLink{ProviderGroup: row.ProviderGroup, Admin: row.Admin}
		return err
	})
	return out, err
}

// run calls one group function as the user, and names its refusal.
func (g *Groups) run(ctx context.Context, call func(*pgdbgen.Queries) error) error {
	err := pgsession.Run(ctx, g.db, g.q, pgsession.Personal(g.user), call)
	if err == nil {
		return nil
	}
	if refusal := refusalOf(err); refusal != nil {
		return refusal
	}
	return fmt.Errorf("manage groups: %w", err)
}
