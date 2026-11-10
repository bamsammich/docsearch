package connectapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	groupv1 "github.com/bamsammich/docsearch/internal/api/docsearch/group/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/group/v1/groupv1connect"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// Groups creates and runs groups as one user. The Postgres repository
// satisfies it, and the database decides every rule.
type Groups interface {
	All(ctx context.Context) ([]sharing.Group, error)
	Invitations(ctx context.Context) ([]sharing.GroupInvitation, error)
	Create(ctx context.Context, slug, name string) (sharing.Group, error)
	Rename(ctx context.Context, slug, name string) error
	Delete(ctx context.Context, slug string) error
	Invite(ctx context.Context, slug, username, role string) (bool, error)
	Force(ctx context.Context, slug, username, role string) error
	Accept(ctx context.Context, slug string) (string, error)
	Decline(ctx context.Context, slug string) error
	Remove(ctx context.Context, slug, username string) error
	Members(ctx context.Context, slug string) ([]sharing.GroupMember, error)
	Libraries(ctx context.Context, slug string) ([]sharing.Library, error)
	Events(ctx context.Context, slug string) ([]sharing.Event, error)
	GrantLibrary(ctx context.Context, library, slug, role string) error
	RevokeLibrary(ctx context.Context, library, slug string) error
	OnLibrary(ctx context.Context, library string) ([]sharing.GroupGrant, error)
	Link(ctx context.Context, slug, providerGroup string) error
	ProviderLink(ctx context.Context, slug string) (sharing.ProviderLink, error)
}

// GroupsFor builds the group manager of the user a request acts for.
type GroupsFor func(user string) (Groups, error)

// MayCreate reports whether user may create groups on this deployment,
// which is an administrator's setting rather than a database rule.
type MayCreate func(ctx context.Context, user string) (bool, error)

var (
	// ErrMayNotCreate is a user the deployment does not let create groups.
	ErrMayNotCreate = errors.New("only docsearch administrators create groups here")
	// ErrNotAdministrator is a provider link asked for by someone who is
	// not an administrator.
	ErrNotAdministrator = errors.New("only a docsearch administrator sees a group's provider link")
	// ErrNotLinking is a link asked of a deployment that reads no groups
	// from its provider.
	ErrNotLinking = errors.New("this deployment reads no groups from its provider: " +
		"set --oidc-groups-claim to link groups")
)

// GroupServer implements the generated group handler.
type GroupServer struct {
	groups    GroupsFor
	mayCreate MayCreate
	linking   bool
}

// NewGroupServer returns the path to mount the handler on, and the handler.
// linking is whether the deployment reads its provider's groups, which a
// link needs.
func NewGroupServer(
	groups GroupsFor,
	mayCreate MayCreate,
	linking bool,
	opts ...connect.HandlerOption,
) (string, http.Handler) {
	return groupv1connect.NewGroupServiceHandler(
		&GroupServer{groups: groups, mayCreate: mayCreate, linking: linking}, opts...)
}

// List is every group, and whether the caller may create one.
func (s *GroupServer) List(
	ctx context.Context,
	_ *connect.Request[groupv1.ListRequest],
) (*connect.Response[groupv1.ListResponse], error) {
	user, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	all, err := groups.All(ctx)
	if err != nil {
		return nil, sharingError(err)
	}
	may, err := s.mayCreate(ctx, user)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &groupv1.ListResponse{Groups: make([]*groupv1.Group, 0, len(all)), CanCreate: may}
	for _, g := range all {
		out.Groups = append(out.Groups, groupMessage(g))
	}
	return connect.NewResponse(out), nil
}

// Create makes a group with the caller as its maintainer.
func (s *GroupServer) Create(
	ctx context.Context,
	req *connect.Request[groupv1.CreateRequest],
) (*connect.Response[groupv1.CreateResponse], error) {
	if req.Msg.GetGroup() == "" || req.Msg.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a group needs a slug and a name people read"))
	}
	user, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	may, err := s.mayCreate(ctx, user)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !may {
		return nil, connect.NewError(connect.CodePermissionDenied, ErrMayNotCreate)
	}
	made, err := groups.Create(ctx, req.Msg.GetGroup(), req.Msg.GetName())
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&groupv1.CreateResponse{Group: groupMessage(made)}), nil
}

// Rename changes the name people read a group by.
func (s *GroupServer) Rename(
	ctx context.Context,
	req *connect.Request[groupv1.RenameRequest],
) (*connect.Response[groupv1.RenameResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := groups.Rename(ctx, req.Msg.GetGroup(), req.Msg.GetName()); err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&groupv1.RenameResponse{}), nil
}

// Delete removes a group and its grants.
func (s *GroupServer) Delete(
	ctx context.Context,
	req *connect.Request[groupv1.DeleteRequest],
) (*connect.Response[groupv1.DeleteResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := groups.Delete(ctx, req.Msg.GetGroup()); err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&groupv1.DeleteResponse{}), nil
}

// Invite asks someone to join, or changes a member's role.
func (s *GroupServer) Invite(
	ctx context.Context,
	req *connect.Request[groupv1.InviteRequest],
) (*connect.Response[groupv1.InviteResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	if msg.GetForce() {
		err = groups.Force(ctx, msg.GetGroup(), msg.GetUsername(), msg.GetRole())
		if err != nil {
			return nil, sharingError(err)
		}
		return connect.NewResponse(&groupv1.InviteResponse{}), nil
	}
	invited, err := groups.Invite(ctx, msg.GetGroup(), msg.GetUsername(), msg.GetRole())
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&groupv1.InviteResponse{Invited: invited}), nil
}

// Accept joins a group the caller is invited to.
func (s *GroupServer) Accept(
	ctx context.Context,
	req *connect.Request[groupv1.AcceptRequest],
) (*connect.Response[groupv1.AcceptResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	role, err := groups.Accept(ctx, req.Msg.GetGroup())
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&groupv1.AcceptResponse{Role: role}), nil
}

// Decline refuses an invitation to a group.
func (s *GroupServer) Decline(
	ctx context.Context,
	req *connect.Request[groupv1.DeclineRequest],
) (*connect.Response[groupv1.DeclineResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := groups.Decline(ctx, req.Msg.GetGroup()); err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&groupv1.DeclineResponse{}), nil
}

// Remove takes someone out of a group, or withdraws their invitation.
func (s *GroupServer) Remove(
	ctx context.Context,
	req *connect.Request[groupv1.RemoveRequest],
) (*connect.Response[groupv1.RemoveResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := groups.Remove(ctx, req.Msg.GetGroup(), req.Msg.GetUsername()); err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&groupv1.RemoveResponse{}), nil
}

// Invitations are the groups the caller is asked to join.
func (s *GroupServer) Invitations(
	ctx context.Context,
	_ *connect.Request[groupv1.InvitationsRequest],
) (*connect.Response[groupv1.InvitationsResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	invitations, err := groups.Invitations(ctx)
	if err != nil {
		return nil, sharingError(err)
	}
	out := &groupv1.InvitationsResponse{
		Invitations: make([]*groupv1.Invitation, 0, len(invitations)),
	}
	for _, i := range invitations {
		out.Invitations = append(out.Invitations, &groupv1.Invitation{
			Group: i.Group, Name: i.Name, Role: i.Role, InvitedBy: i.InvitedBy,
			At: timestamppb.New(i.At),
		})
	}
	return connect.NewResponse(out), nil
}

// Members are who is in a group.
func (s *GroupServer) Members(
	ctx context.Context,
	req *connect.Request[groupv1.MembersRequest],
) (*connect.Response[groupv1.MembersResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	members, err := groups.Members(ctx, req.Msg.GetGroup())
	if err != nil {
		return nil, sharingError(err)
	}
	out := &groupv1.MembersResponse{Members: make([]*groupv1.Member, 0, len(members))}
	for _, m := range members {
		out.Members = append(out.Members, &groupv1.Member{
			Username: m.Username, Role: m.Role, FromProvider: m.FromProvider, Invited: m.Invited,
		})
	}
	return connect.NewResponse(out), nil
}

// Libraries are the libraries granted to a group.
func (s *GroupServer) Libraries(
	ctx context.Context,
	req *connect.Request[groupv1.LibrariesRequest],
) (*connect.Response[groupv1.LibrariesResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	libs, err := groups.Libraries(ctx, req.Msg.GetGroup())
	if err != nil {
		return nil, sharingError(err)
	}
	out := &groupv1.LibrariesResponse{Libraries: make([]*groupv1.Library, 0, len(libs))}
	for _, l := range libs {
		out.Libraries = append(out.Libraries,
			&groupv1.Library{Library: l.Name, Name: l.Title, Role: l.Role})
	}
	return connect.NewResponse(out), nil
}

// Events is the record of changes to a group.
func (s *GroupServer) Events(
	ctx context.Context,
	req *connect.Request[groupv1.EventsRequest],
) (*connect.Response[groupv1.EventsResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	events, err := groups.Events(ctx, req.Msg.GetGroup())
	if err != nil {
		return nil, sharingError(err)
	}
	out := &groupv1.EventsResponse{Events: make([]*groupv1.Event, 0, len(events))}
	for _, e := range events {
		out.Events = append(out.Events, &groupv1.Event{
			At: timestamppb.New(e.At), Actor: e.Actor, Action: e.Action,
			Subject: e.Subject, Role: e.Role, Detail: e.Detail,
		})
	}
	return connect.NewResponse(out), nil
}

// ProviderLink is the provider group a group follows, for an
// administrator.
func (s *GroupServer) ProviderLink(
	ctx context.Context,
	req *connect.Request[groupv1.ProviderLinkRequest],
) (*connect.Response[groupv1.ProviderLinkResponse], error) {
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	link, err := groups.ProviderLink(ctx, req.Msg.GetGroup())
	if err != nil {
		return nil, sharingError(err)
	}
	if !link.Admin {
		return nil, connect.NewError(connect.CodePermissionDenied, ErrNotAdministrator)
	}
	return connect.NewResponse(&groupv1.ProviderLinkResponse{
		ProviderGroup: link.ProviderGroup,
		CanLink:       s.linking && link.Admin,
	}), nil
}

// Link makes a group follow a provider group, or stops it following one.
func (s *GroupServer) Link(
	ctx context.Context,
	req *connect.Request[groupv1.LinkRequest],
) (*connect.Response[groupv1.LinkResponse], error) {
	if !s.linking {
		return nil, connect.NewError(connect.CodeFailedPrecondition, ErrNotLinking)
	}
	_, groups, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	err = groups.Link(ctx, req.Msg.GetGroup(), strings.TrimSpace(req.Msg.GetProviderGroup()))
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&groupv1.LinkResponse{}), nil
}

//nolint:ireturn // a factory for a port returns that port; a concrete type here would defeat it.
func (s *GroupServer) forOwner(ctx context.Context) (string, Groups, error) {
	user, err := owner.From(ctx)
	if err != nil {
		return "", nil, asConnectError(err)
	}
	groups, err := s.groups(user)
	if err != nil {
		return "", nil, asConnectError(err)
	}
	return user, groups, nil
}

func groupMessage(g sharing.Group) *groupv1.Group {
	return &groupv1.Group{Group: g.Slug, Name: g.Name, Role: g.Role, Members: g.Members}
}
