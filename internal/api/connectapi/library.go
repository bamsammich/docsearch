package connectapi

import (
	"context"
	"errors"
	"net/http"

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
	Grant(ctx context.Context, slug, username, role string) error
	Revoke(ctx context.Context, slug, username string) error
	Events(ctx context.Context, slug string) ([]sharing.Event, error)
}

// LibrariesFor builds the library manager of the user a request acts for.
type LibrariesFor func(user string) (Libraries, error)

// LibraryServer implements the generated library handler.
type LibraryServer struct {
	libraries LibrariesFor
}

// NewLibraryServer returns the path to mount the handler on, and the handler.
func NewLibraryServer(
	libraries LibrariesFor,
	opts ...connect.HandlerOption,
) (string, http.Handler) {
	return libraryv1connect.NewLibraryServiceHandler(&LibraryServer{libraries: libraries}, opts...)
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
		out.Members = append(out.Members, &libraryv1.Member{Username: m.Username, Role: m.Role})
	}
	return connect.NewResponse(out), nil
}

// Grant gives someone a role on a library, or changes theirs.
func (s *LibraryServer) Grant(
	ctx context.Context,
	req *connect.Request[libraryv1.GrantRequest],
) (*connect.Response[libraryv1.GrantResponse], error) {
	libs, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	if err := libs.Grant(ctx, msg.GetLibrary(), msg.GetUsername(), msg.GetRole()); err != nil {
		return nil, sharingError(err)
	}
	return connect.NewResponse(&libraryv1.GrantResponse{}), nil
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
	sharing.ErrNoLibrary: connect.CodeNotFound,
	sharing.ErrNoAccount: connect.CodeNotFound,
	sharing.ErrLastAdmin: connect.CodeFailedPrecondition,
	sharing.ErrNameTaken: connect.CodeAlreadyExists,
	sharing.ErrInvalid:   connect.CodeInvalidArgument,
}

func sharingError(err error) error {
	for refusal, code := range sharingCodes {
		if errors.Is(err, refusal) {
			return connect.NewError(code, err)
		}
	}
	return connect.NewError(connect.CodeInternal, err)
}
