package connectclient

import (
	"context"
	"time"

	"connectrpc.com/connect"

	libraryv1 "github.com/bamsammich/docsearch/internal/api/docsearch/library/v1"
	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// Libraries is every library the caller reads, their own first.
func (c *Client) Libraries(ctx context.Context) ([]sharing.Library, error) {
	resp, err := c.libraries.List(ctx, connect.NewRequest(&libraryv1.ListRequest{}))
	if err != nil {
		return nil, err
	}
	out := make([]sharing.Library, 0, len(resp.Msg.GetLibraries()))
	for _, lib := range resp.Msg.GetLibraries() {
		out = append(out, libraryOf(lib))
	}
	return out, nil
}

// CreateLibrary makes a shared library with the caller as its admin.
func (c *Client) CreateLibrary(ctx context.Context, slug, name string) (sharing.Library, error) {
	resp, err := c.libraries.Create(ctx,
		connect.NewRequest(&libraryv1.CreateRequest{Slug: slug, Name: name}))
	if err != nil {
		return sharing.Library{}, err
	}
	return libraryOf(resp.Msg.GetLibrary()), nil
}

// Members is everyone with a role on a library the caller reads.
func (c *Client) Members(ctx context.Context, library string) ([]sharing.Member, error) {
	resp, err := c.libraries.Members(ctx,
		connect.NewRequest(&libraryv1.MembersRequest{Library: library}))
	if err != nil {
		return nil, err
	}
	out := make([]sharing.Member, 0, len(resp.Msg.GetMembers()))
	for _, m := range resp.Msg.GetMembers() {
		out = append(out, sharing.Member{
			Username: m.GetUsername(),
			Role:     m.GetRole(),
			Invited:  m.GetInvited(),
		})
	}
	return out, nil
}

// Grant changes username's role on library, or invites them with role when
// they hold none. It reports whether it invited.
func (c *Client) Grant(ctx context.Context, library, username, role string) (bool, error) {
	resp, err := c.libraries.Grant(ctx, connect.NewRequest(
		&libraryv1.GrantRequest{Library: library, Username: username, Role: role}))
	if err != nil {
		return false, err
	}
	return resp.Msg.GetInvited(), nil
}

// Invitations are the libraries the caller is asked to join, newest first.
func (c *Client) Invitations(ctx context.Context) ([]sharing.Invitation, error) {
	resp, err := c.libraries.Invitations(ctx, connect.NewRequest(&libraryv1.InvitationsRequest{}))
	if err != nil {
		return nil, err
	}
	out := make([]sharing.Invitation, 0, len(resp.Msg.GetInvitations()))
	for _, i := range resp.Msg.GetInvitations() {
		out = append(out, sharing.Invitation{
			At: i.GetAt().AsTime(), Library: i.GetLibrary(), Title: i.GetName(),
			Role: i.GetRole(), InvitedBy: i.GetInvitedBy(),
		})
	}
	return out, nil
}

// Accept joins library with the role its invitation offered.
func (c *Client) Accept(ctx context.Context, library string) (sharing.Library, error) {
	resp, err := c.libraries.Accept(ctx,
		connect.NewRequest(&libraryv1.AcceptRequest{Library: library}))
	if err != nil {
		return sharing.Library{}, err
	}
	return libraryOf(resp.Msg.GetLibrary()), nil
}

// Decline refuses the invitation to library.
func (c *Client) Decline(ctx context.Context, library string) error {
	_, err := c.libraries.Decline(ctx,
		connect.NewRequest(&libraryv1.DeclineRequest{Library: library}))
	return err
}

// Revoke takes username's role on library away.
func (c *Client) Revoke(ctx context.Context, library, username string) error {
	_, err := c.libraries.Revoke(ctx, connect.NewRequest(
		&libraryv1.RevokeRequest{Library: library, Username: username}))
	return err
}

// Events is the record of who changed which role on library.
func (c *Client) Events(ctx context.Context, library string) ([]sharing.Event, error) {
	resp, err := c.libraries.Events(ctx,
		connect.NewRequest(&libraryv1.EventsRequest{Library: library}))
	if err != nil {
		return nil, err
	}
	out := make([]sharing.Event, 0, len(resp.Msg.GetEvents()))
	for _, e := range resp.Msg.GetEvents() {
		out = append(out, sharing.Event{
			At: e.GetAt().AsTime(), Actor: e.GetActor(), Action: e.GetAction(),
			Subject: e.GetSubject(), Role: e.GetRole(), Detail: e.GetDetail(),
		})
	}
	return out, nil
}

// Rename changes the name people read library by.
func (c *Client) Rename(ctx context.Context, library, name string) (sharing.Library, error) {
	resp, err := c.libraries.Rename(ctx,
		connect.NewRequest(&libraryv1.RenameRequest{Library: library, Name: name}))
	if err != nil {
		return sharing.Library{}, err
	}
	return libraryOf(resp.Msg.GetLibrary()), nil
}

// Delete deletes library, and answers when it is purged unless restored.
func (c *Client) Delete(ctx context.Context, library string) (time.Time, error) {
	resp, err := c.libraries.Delete(ctx,
		connect.NewRequest(&libraryv1.DeleteRequest{Library: library}))
	if err != nil {
		return time.Time{}, err
	}
	return resp.Msg.GetPurgeAt().AsTime(), nil
}

// Restore brings back the deleted library.
func (c *Client) Restore(ctx context.Context, library string) (sharing.Library, error) {
	resp, err := c.libraries.Restore(ctx,
		connect.NewRequest(&libraryv1.RestoreRequest{Library: library}))
	if err != nil {
		return sharing.Library{}, err
	}
	return libraryOf(resp.Msg.GetLibrary()), nil
}

// Deleted are the deleted libraries the caller can restore.
func (c *Client) Deleted(ctx context.Context) ([]sharing.Deleted, error) {
	resp, err := c.libraries.Deleted(ctx, connect.NewRequest(&libraryv1.DeletedRequest{}))
	if err != nil {
		return nil, err
	}
	out := make([]sharing.Deleted, 0, len(resp.Msg.GetLibraries()))
	for _, d := range resp.Msg.GetLibraries() {
		out = append(out, sharing.Deleted{
			DeletedAt: d.GetDeletedAt().AsTime(), PurgeAt: d.GetPurgeAt().AsTime(),
			Library: d.GetLibrary(), Title: d.GetName(), DeletedBy: d.GetDeletedBy(),
		})
	}
	return out, nil
}

func libraryOf(lib *libraryv1.Library) sharing.Library {
	return sharing.Library{Name: lib.GetLibrary(), Title: lib.GetName(), Role: lib.GetRole()}
}
