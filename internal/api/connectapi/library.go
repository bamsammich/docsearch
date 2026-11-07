package connectapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	libraryv1 "github.com/bamsammich/docsearch/internal/api/docsearch/library/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/library/v1/libraryv1connect"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// Libraries creates and shares libraries as one user. The port is declared
// here because this package is what calls it; the Postgres repository
// satisfies it, and the database decides every rule.
type Libraries interface {
	List(ctx context.Context) ([]sharing.Library, error)
	Username(ctx context.Context) (string, error)
	Create(ctx context.Context, slug, name string) (sharing.Library, error)
	Members(ctx context.Context, slug string) ([]sharing.Member, error)
	Grant(ctx context.Context, slug, username, role string) (bool, error)
	Revoke(ctx context.Context, slug, username string) error
	Events(ctx context.Context, slug string) ([]sharing.Event, error)
	Invitations(ctx context.Context) ([]sharing.Invitation, error)
	Accept(ctx context.Context, slug string) (sharing.Library, error)
	Decline(ctx context.Context, slug string) error
	Rename(ctx context.Context, slug, name string) (sharing.Library, error)
	Delete(ctx context.Context, slug string) (time.Time, error)
	Restore(ctx context.Context, slug string) (sharing.Library, error)
	Deleted(ctx context.Context) ([]sharing.Deleted, error)
}

// LibrariesFor builds the library manager of the user a request acts for.
type LibrariesFor func(user string) (Libraries, error)

// LibraryServer implements the generated library handler.
type LibraryServer struct {
	libraries LibrariesFor
	groups    GroupsFor
}

// NewLibraryServer returns the path to mount the handler on, and the handler.
func NewLibraryServer(
	libraries LibrariesFor,
	groups GroupsFor,
	opts ...connect.HandlerOption,
) (string, http.Handler) {
	return libraryv1connect.NewLibraryServiceHandler(
		&LibraryServer{libraries: libraries, groups: groups}, opts...)
}

// GrantGroup gives a group a role on a library.
func (s *LibraryServer) GrantGroup(
	ctx context.Context,
	req *connect.Request[libraryv1.GrantGroupRequest],
) (*connect.Response[libraryv1.GrantGroupResponse], error) {
	groups, err := s.groupsForOwner(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	err = groups.GrantLibrary(ctx, msg.GetLibrary(), msg.GetGroup(), msg.GetRole())
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.GrantGroupResponse{}), nil
}

// RevokeGroup takes a group's role on a library away.
func (s *LibraryServer) RevokeGroup(
	ctx context.Context,
	req *connect.Request[libraryv1.RevokeGroupRequest],
) (*connect.Response[libraryv1.RevokeGroupResponse], error) {
	groups, err := s.groupsForOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := groups.RevokeLibrary(ctx, req.Msg.GetLibrary(), req.Msg.GetGroup()); err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.RevokeGroupResponse{}), nil
}

// Groups are the groups a library is granted to.
func (s *LibraryServer) Groups(
	ctx context.Context,
	req *connect.Request[libraryv1.GroupsRequest],
) (*connect.Response[libraryv1.GroupsResponse], error) {
	groups, err := s.groupsForOwner(ctx)
	if err != nil {
		return nil, err
	}
	grants, err := groups.OnLibrary(ctx, req.Msg.GetLibrary())
	if err != nil {
		return nil, sharingError(err)
	}
	out := &libraryv1.GroupsResponse{Groups: make([]*libraryv1.GroupGrant, 0, len(grants))}
	for _, g := range grants {
		out.Groups = append(out.Groups, &libraryv1.GroupGrant{
			Group: g.Group, Name: g.Name, Role: g.Role, Members: g.Members,
		})
	}
	return connect.NewResponse(out), nil
}

//nolint:ireturn // a factory for a port returns that port; a concrete type here would defeat it.
func (s *LibraryServer) groupsForOwner(ctx context.Context) (Groups, error) {
	user, err := owner.From(ctx)
	if err != nil {
		return nil, asConnectError(err)
	}
	groups, err := s.groups(user)
	if err != nil {
		return nil, asConnectError(err)
	}
	return groups, nil
}

// List is every library the caller reads.
func (s *LibraryServer) List(
	ctx context.Context,
	_ *connect.Request[libraryv1.ListRequest],
) (*connect.Response[libraryv1.ListResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	listed, err := libs.List(ctx)
	if err != nil {
		return nil, sharingError(err)
	}
	username, err := libs.Username(ctx)
	if err != nil {
		return nil, sharingError(err)
	}
	out := &libraryv1.ListResponse{
		Libraries: make([]*libraryv1.Library, 0, len(listed)),
		Username:  username,
	}
	for _, lib := range listed {
		out.Libraries = append(out.Libraries, libraryMessage(lib))
	}
	return connect.NewResponse(out), nil
}

// Create makes a shared library with the caller as its admin.
func (s *LibraryServer) Create(
	ctx context.Context,
	req *connect.Request[libraryv1.CreateRequest],
) (*connect.Response[libraryv1.CreateResponse], error) {
	if req.Msg.GetSlug() == "" || req.Msg.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a library needs a slug agents type and a name people read"))
	}
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	made, err := libs.Create(ctx, req.Msg.GetSlug(), req.Msg.GetName())
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.CreateResponse{Library: libraryMessage(made)}), nil
}

// Members is everyone with a role on a library the caller reads.
func (s *LibraryServer) Members(
	ctx context.Context,
	req *connect.Request[libraryv1.MembersRequest],
) (*connect.Response[libraryv1.MembersResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	members, err := libs.Members(ctx, req.Msg.GetLibrary())
	if err != nil {
		return nil, sharingError(err)
	}
	out := &libraryv1.MembersResponse{Members: make([]*libraryv1.Member, 0, len(members))}
	for _, m := range members {
		out.Members = append(out.Members, &libraryv1.Member{
			Username: m.Username,
			Role:     m.Role,
			Invited:  m.Invited,
		})
	}
	return connect.NewResponse(out), nil
}

// Grant changes the role of someone who holds one, and invites anyone else.
func (s *LibraryServer) Grant(
	ctx context.Context,
	req *connect.Request[libraryv1.GrantRequest],
) (*connect.Response[libraryv1.GrantResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	invited, err := libs.Grant(ctx, msg.GetLibrary(), msg.GetUsername(), msg.GetRole())
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.GrantResponse{Invited: invited}), nil
}

// Invitations are the libraries the caller is asked to join.
func (s *LibraryServer) Invitations(
	ctx context.Context,
	_ *connect.Request[libraryv1.InvitationsRequest],
) (*connect.Response[libraryv1.InvitationsResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	invitations, err := libs.Invitations(ctx)
	if err != nil {
		return nil, sharingError(err)
	}
	out := &libraryv1.InvitationsResponse{
		Invitations: make([]*libraryv1.Invitation, 0, len(invitations)),
	}
	for _, i := range invitations {
		out.Invitations = append(out.Invitations, &libraryv1.Invitation{
			Library:   i.Library,
			Name:      i.Title,
			Role:      i.Role,
			InvitedBy: i.InvitedBy,
			At:        timestamppb.New(i.At),
		})
	}
	return connect.NewResponse(out), nil
}

// Accept joins a library the caller is invited to.
func (s *LibraryServer) Accept(
	ctx context.Context,
	req *connect.Request[libraryv1.AcceptRequest],
) (*connect.Response[libraryv1.AcceptResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	joined, err := libs.Accept(ctx, req.Msg.GetLibrary())
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.AcceptResponse{Library: libraryMessage(joined)}), nil
}

// Rename changes the name people read a library by.
func (s *LibraryServer) Rename(
	ctx context.Context,
	req *connect.Request[libraryv1.RenameRequest],
) (*connect.Response[libraryv1.RenameResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	renamed, err := libs.Rename(ctx, req.Msg.GetLibrary(), req.Msg.GetName())
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.RenameResponse{Library: libraryMessage(renamed)}), nil
}

// Delete deletes a shared library, restorable until it is purged.
func (s *LibraryServer) Delete(
	ctx context.Context,
	req *connect.Request[libraryv1.DeleteRequest],
) (*connect.Response[libraryv1.DeleteResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	at, err := libs.Delete(ctx, req.Msg.GetLibrary())
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.DeleteResponse{PurgeAt: timestamppb.New(at)}), nil
}

// Restore brings back a deleted library.
func (s *LibraryServer) Restore(
	ctx context.Context,
	req *connect.Request[libraryv1.RestoreRequest],
) (*connect.Response[libraryv1.RestoreResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	restored, err := libs.Restore(ctx, req.Msg.GetLibrary())
	if err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.RestoreResponse{Library: libraryMessage(restored)}), nil
}

// Deleted are the deleted libraries the caller can restore.
func (s *LibraryServer) Deleted(
	ctx context.Context,
	_ *connect.Request[libraryv1.DeletedRequest],
) (*connect.Response[libraryv1.DeletedResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := libs.Deleted(ctx)
	if err != nil {
		return nil, sharingError(err)
	}
	out := &libraryv1.DeletedResponse{Libraries: make([]*libraryv1.DeletedLibrary, 0, len(deleted))}
	for _, d := range deleted {
		out.Libraries = append(out.Libraries, &libraryv1.DeletedLibrary{
			Library:   d.Library,
			Name:      d.Title,
			DeletedAt: timestamppb.New(d.DeletedAt),
			PurgeAt:   timestamppb.New(d.PurgeAt),
			DeletedBy: d.DeletedBy,
		})
	}
	return connect.NewResponse(out), nil
}

// Decline refuses an invitation.
func (s *LibraryServer) Decline(
	ctx context.Context,
	req *connect.Request[libraryv1.DeclineRequest],
) (*connect.Response[libraryv1.DeclineResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := libs.Decline(ctx, req.Msg.GetLibrary()); err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.DeclineResponse{}), nil
}

// Revoke takes someone's role on a library away.
func (s *LibraryServer) Revoke(
	ctx context.Context,
	req *connect.Request[libraryv1.RevokeRequest],
) (*connect.Response[libraryv1.RevokeResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := libs.Revoke(ctx, req.Msg.GetLibrary(), req.Msg.GetUsername()); err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.RevokeResponse{}), nil
}

// Events is the record of who changed which role on a library.
func (s *LibraryServer) Events(
	ctx context.Context,
	req *connect.Request[libraryv1.EventsRequest],
) (*connect.Response[libraryv1.EventsResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	events, err := libs.Events(ctx, req.Msg.GetLibrary())
	if err != nil {
		return nil, sharingError(err)
	}
	out := &libraryv1.EventsResponse{Events: make([]*libraryv1.Event, 0, len(events))}
	for _, e := range events {
		out.Events = append(out.Events, &libraryv1.Event{
			At: timestamppb.New(
				e.At,
			),
			Actor:   e.Actor,
			Action:  e.Action,
			Subject: e.Subject,
			Role:    e.Role,
			Detail:  e.Detail,
		})
	}
	return connect.NewResponse(out), nil
}

//nolint:ireturn // a factory for a port returns that port; a concrete type here would defeat it.
func (s *LibraryServer) forOwner(ctx context.Context) (Libraries, error) {
	user, err := owner.From(ctx)
	if err != nil {
		return nil, asConnectError(err)
	}
	libs, err := s.libraries(user)
	if err != nil {
		return nil, asConnectError(err)
	}
	return libs, nil
}

func libraryMessage(lib sharing.Library) *libraryv1.Library {
	return &libraryv1.Library{Library: lib.Name, Name: lib.Title, Role: lib.Role}
}

// sharingCodes are the Connect codes the database's refusals answer with.
var sharingCodes = map[error]connect.Code{
	sharing.ErrNoLibrary:         connect.CodeNotFound,
	sharing.ErrNoAccount:         connect.CodeNotFound,
	sharing.ErrNoInvitation:      connect.CodeNotFound,
	sharing.ErrTooMany:           connect.CodeResourceExhausted,
	sharing.ErrNoGroup:           connect.CodeNotFound,
	sharing.ErrNoGroupInvitation: connect.CodeNotFound,
	sharing.ErrLastMaintainer:    connect.CodeFailedPrecondition,
	sharing.ErrFromProvider:      connect.CodeFailedPrecondition,
	sharing.ErrGroupTaken:        connect.CodeAlreadyExists,
	sharing.ErrGroupRole:         connect.CodeInvalidArgument,
	sharing.ErrGroupName:         connect.CodeInvalidArgument,
	sharing.ErrJoinByForce:       connect.CodeFailedPrecondition,
	sharing.ErrLastAdmin:         connect.CodeFailedPrecondition,
	sharing.ErrNameTaken:         connect.CodeAlreadyExists,
	sharing.ErrInvalid:           connect.CodeInvalidArgument,
	sharing.ErrLibraryName:       connect.CodeInvalidArgument,
}

func sharingError(err error) error {
	for refusal, code := range sharingCodes {
		if errors.Is(err, refusal) {
			return connect.NewError(code, err)
		}
	}
	return connect.NewError(connect.CodeInternal, err)
}
