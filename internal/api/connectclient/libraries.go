package connectclient

import (
	"context"

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
		out = append(out, sharing.Member{Username: m.GetUsername(), Role: m.GetRole()})
	}
	return out, nil
}

// Grant gives username role on library, or changes theirs.
func (c *Client) Grant(ctx context.Context, library, username, role string) error {
	_, err := c.libraries.Grant(ctx, connect.NewRequest(
		&libraryv1.GrantRequest{Library: library, Username: username, Role: role}))
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
			Subject: e.GetSubject(), Role: e.GetRole(),
		})
	}
	return out, nil
}

func libraryOf(lib *libraryv1.Library) sharing.Library {
	return sharing.Library{Name: lib.GetLibrary(), Title: lib.GetName(), Role: lib.GetRole()}
}
