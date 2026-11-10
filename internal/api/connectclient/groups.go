package connectclient

import (
	"context"

	"connectrpc.com/connect"

	groupv1 "github.com/bamsammich/docsearch/internal/api/docsearch/group/v1"
	libraryv1 "github.com/bamsammich/docsearch/internal/api/docsearch/library/v1"
	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// Groups is every group with the caller's role in each, and whether the
// caller may create one.
func (c *Client) Groups(ctx context.Context) ([]sharing.Group, bool, error) {
	resp, err := c.groups.List(ctx, connect.NewRequest(&groupv1.ListRequest{}))
	if err != nil {
		return nil, false, err
	}
	out := make([]sharing.Group, 0, len(resp.Msg.GetGroups()))
	for _, g := range resp.Msg.GetGroups() {
		out = append(out, groupOf(g))
	}
	return out, resp.Msg.GetCanCreate(), nil
}

// CreateGroup makes a group with the caller as its maintainer.
func (c *Client) CreateGroup(ctx context.Context, slug, name string) (sharing.Group, error) {
	resp, err := c.groups.Create(ctx,
		connect.NewRequest(&groupv1.CreateRequest{Group: slug, Name: name}))
	if err != nil {
		return sharing.Group{}, err
	}
	return groupOf(resp.Msg.GetGroup()), nil
}

// RenameGroup changes the name people read a group by.
func (c *Client) RenameGroup(ctx context.Context, slug, name string) error {
	_, err := c.groups.Rename(ctx,
		connect.NewRequest(&groupv1.RenameRequest{Group: slug, Name: name}))
	return err
}

// DeleteGroup removes a group and its grants.
func (c *Client) DeleteGroup(ctx context.Context, slug string) error {
	_, err := c.groups.Delete(ctx, connect.NewRequest(&groupv1.DeleteRequest{Group: slug}))
	return err
}

// InviteToGroup asks username to join with role, or changes a member's
// role. It reports whether it invited.
func (c *Client) InviteToGroup(ctx context.Context, slug, username, role string) (bool, error) {
	resp, err := c.groups.Invite(ctx, connect.NewRequest(
		&groupv1.InviteRequest{Group: slug, Username: username, Role: role}))
	if err != nil {
		return false, err
	}
	return resp.Msg.GetInvited(), nil
}

// ForceIntoGroup makes someone a member at once, with no invitation:
// an administrator's breakglass, which the group and its libraries log.
func (c *Client) ForceIntoGroup(ctx context.Context, slug, username, role string) error {
	_, err := c.groups.Invite(ctx, connect.NewRequest(
		&groupv1.InviteRequest{Group: slug, Username: username, Role: role, Force: true}))
	return err
}

// AcceptGroup joins a group the caller is invited to, and answers the role.
func (c *Client) AcceptGroup(ctx context.Context, slug string) (string, error) {
	resp, err := c.groups.Accept(ctx, connect.NewRequest(&groupv1.AcceptRequest{Group: slug}))
	if err != nil {
		return "", err
	}
	return resp.Msg.GetRole(), nil
}

// DeclineGroup refuses an invitation to a group.
func (c *Client) DeclineGroup(ctx context.Context, slug string) error {
	_, err := c.groups.Decline(ctx, connect.NewRequest(&groupv1.DeclineRequest{Group: slug}))
	return err
}

// RemoveFromGroup takes username out of a group, or withdraws their
// invitation.
func (c *Client) RemoveFromGroup(ctx context.Context, slug, username string) error {
	_, err := c.groups.Remove(ctx,
		connect.NewRequest(&groupv1.RemoveRequest{Group: slug, Username: username}))
	return err
}

// GroupInvitations are the groups the caller is asked to join.
func (c *Client) GroupInvitations(ctx context.Context) ([]sharing.GroupInvitation, error) {
	resp, err := c.groups.Invitations(ctx, connect.NewRequest(&groupv1.InvitationsRequest{}))
	if err != nil {
		return nil, err
	}
	out := make([]sharing.GroupInvitation, 0, len(resp.Msg.GetInvitations()))
	for _, i := range resp.Msg.GetInvitations() {
		out = append(out, sharing.GroupInvitation{
			At: i.GetAt().AsTime(), Group: i.GetGroup(), Name: i.GetName(),
			Role: i.GetRole(), InvitedBy: i.GetInvitedBy(),
		})
	}
	return out, nil
}

// GroupMembers are who is in a group.
func (c *Client) GroupMembers(ctx context.Context, slug string) ([]sharing.GroupMember, error) {
	resp, err := c.groups.Members(ctx, connect.NewRequest(&groupv1.MembersRequest{Group: slug}))
	if err != nil {
		return nil, err
	}
	out := make([]sharing.GroupMember, 0, len(resp.Msg.GetMembers()))
	for _, m := range resp.Msg.GetMembers() {
		out = append(out, sharing.GroupMember{
			Username: m.GetUsername(), Role: m.GetRole(),
			FromProvider: m.GetFromProvider(), Invited: m.GetInvited(),
		})
	}
	return out, nil
}

// GroupLibraries are the libraries granted to a group.
func (c *Client) GroupLibraries(ctx context.Context, slug string) ([]sharing.Library, error) {
	resp, err := c.groups.Libraries(ctx,
		connect.NewRequest(&groupv1.LibrariesRequest{Group: slug}))
	if err != nil {
		return nil, err
	}
	out := make([]sharing.Library, 0, len(resp.Msg.GetLibraries()))
	for _, l := range resp.Msg.GetLibraries() {
		out = append(out, sharing.Library{
			Name: l.GetLibrary(), Title: l.GetName(), Role: l.GetRole(),
		})
	}
	return out, nil
}

// GroupEvents is the record of changes to a group.
func (c *Client) GroupEvents(ctx context.Context, slug string) ([]sharing.Event, error) {
	resp, err := c.groups.Events(ctx, connect.NewRequest(&groupv1.EventsRequest{Group: slug}))
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

// GrantGroup gives a group role on library, or changes it.
func (c *Client) GrantGroup(ctx context.Context, library, slug, role string) error {
	_, err := c.libraries.GrantGroup(ctx, connect.NewRequest(
		&libraryv1.GrantGroupRequest{Library: library, Group: slug, Role: role}))
	return err
}

// RevokeGroup takes a group's role on library away.
func (c *Client) RevokeGroup(ctx context.Context, library, slug string) error {
	_, err := c.libraries.RevokeGroup(ctx, connect.NewRequest(
		&libraryv1.RevokeGroupRequest{Library: library, Group: slug}))
	return err
}

// LibraryGroups are the groups library is granted to.
func (c *Client) LibraryGroups(ctx context.Context, library string) ([]sharing.GroupGrant, error) {
	resp, err := c.libraries.Groups(ctx,
		connect.NewRequest(&libraryv1.GroupsRequest{Library: library}))
	if err != nil {
		return nil, err
	}
	out := make([]sharing.GroupGrant, 0, len(resp.Msg.GetGroups()))
	for _, g := range resp.Msg.GetGroups() {
		out = append(out, sharing.GroupGrant{
			Group: g.GetGroup(), Name: g.GetName(), Role: g.GetRole(), Members: g.GetMembers(),
		})
	}
	return out, nil
}

// LinkGroup makes a group follow providerGroup, or stops it following one
// when providerGroup is empty.
func (c *Client) LinkGroup(ctx context.Context, slug, providerGroup string) error {
	_, err := c.groups.Link(ctx, connect.NewRequest(
		&groupv1.LinkRequest{Group: slug, ProviderGroup: providerGroup}))
	return err
}

// GroupProviderLink is the provider group a group follows, and whether the
// caller may change it.
func (c *Client) GroupProviderLink(ctx context.Context, slug string) (string, bool, error) {
	resp, err := c.groups.ProviderLink(ctx,
		connect.NewRequest(&groupv1.ProviderLinkRequest{Group: slug}))
	if err != nil {
		return "", false, err
	}
	return resp.Msg.GetProviderGroup(), resp.Msg.GetCanLink(), nil
}

func groupOf(g *groupv1.Group) sharing.Group {
	return sharing.Group{
		Slug: g.GetGroup(), Name: g.GetName(), Role: g.GetRole(), Members: g.GetMembers(),
	}
}
