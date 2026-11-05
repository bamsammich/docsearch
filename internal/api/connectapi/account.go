package connectapi

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	accountv1 "github.com/bamsammich/docsearch/internal/api/docsearch/account/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/account/v1/accountv1connect"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/service/admin"
)

// Admin is the administrator's work as one user. The port is declared here
// because this package is what calls it; internal/service/admin.Service
// satisfies it.
type Admin interface {
	Me(ctx context.Context) (admin.Me, error)
	List(ctx context.Context) ([]admin.Account, error)
	Add(ctx context.Context, username string) (string, error)
	Reset(ctx context.Context, username string) (string, error)
	Unlock(ctx context.Context, username string) error
	Remove(ctx context.Context, username string) error
	SetAdmin(ctx context.Context, username string, admin bool) error
}

// AdminFor builds the administrator's service for the user a request acts
// for.
type AdminFor func(user string) (Admin, error)

// Setting is one sign-in setting the server's configuration holds, where its
// value came from, and the flag and environment variable that set it. A
// rule the server keeps has no origin and no flag.
type Setting struct {
	Origin config.Origin
	Label  string
	Value  string
	Flag   string
	Env    string
}

// AccountServer implements the generated account handler.
type AccountServer struct {
	admins AdminFor
	// auth is the sign-in mode, file is the docsearch.yaml the server read
	// if any, and settings are how its sign-in is configured. All three are
	// fixed when the server starts.
	auth     string
	file     string
	settings []Setting
}

// NewAccountServer returns the path to mount the handler on, and the handler.
func NewAccountServer(
	admins AdminFor,
	auth string,
	settings []Setting,
	file string,
	opts ...connect.HandlerOption,
) (string, http.Handler) {
	return accountv1connect.NewAccountServiceHandler(
		&AccountServer{admins: admins, auth: auth, settings: settings, file: file}, opts...)
}

func (s *AccountServer) Me(
	ctx context.Context,
	_ *connect.Request[accountv1.MeRequest],
) (*connect.Response[accountv1.MeResponse], error) {
	admins, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	me, err := admins.Me(ctx)
	if err != nil {
		return nil, adminError(err)
	}
	return connect.NewResponse(&accountv1.MeResponse{
		Username: me.Username, Admin: me.Admin, Auth: s.auth,
	}), nil
}

func (s *AccountServer) List(
	ctx context.Context,
	_ *connect.Request[accountv1.ListRequest],
) (*connect.Response[accountv1.ListResponse], error) {
	admins, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := admins.List(ctx)
	if err != nil {
		return nil, adminError(err)
	}
	out := &accountv1.ListResponse{Accounts: make([]*accountv1.Account, 0, len(accounts))}
	for _, a := range accounts {
		out.Accounts = append(out.Accounts, &accountv1.Account{
			Username:    a.Username,
			Admin:       a.Admin,
			CreatedAt:   timestampOf(a.CreatedAt),
			Password:    a.Password,
			LockedUntil: timestampOf(a.LockedUntil),
		})
	}
	return connect.NewResponse(out), nil
}

func (s *AccountServer) Add(
	ctx context.Context,
	req *connect.Request[accountv1.AddRequest],
) (*connect.Response[accountv1.AddResponse], error) {
	admins, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	secret, err := admins.Add(ctx, req.Msg.GetUsername())
	if err != nil {
		return nil, adminError(err)
	}
	return connect.NewResponse(&accountv1.AddResponse{Password: secret}), nil
}

func (s *AccountServer) Reset(
	ctx context.Context,
	req *connect.Request[accountv1.ResetRequest],
) (*connect.Response[accountv1.ResetResponse], error) {
	admins, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	secret, err := admins.Reset(ctx, req.Msg.GetUsername())
	if err != nil {
		return nil, adminError(err)
	}
	return connect.NewResponse(&accountv1.ResetResponse{Password: secret}), nil
}

func (s *AccountServer) Unlock(
	ctx context.Context,
	req *connect.Request[accountv1.UnlockRequest],
) (*connect.Response[accountv1.UnlockResponse], error) {
	admins, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := admins.Unlock(ctx, req.Msg.GetUsername()); err != nil {
		return nil, adminError(err)
	}
	return connect.NewResponse(&accountv1.UnlockResponse{}), nil
}

func (s *AccountServer) Remove(
	ctx context.Context,
	req *connect.Request[accountv1.RemoveRequest],
) (*connect.Response[accountv1.RemoveResponse], error) {
	admins, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := admins.Remove(ctx, req.Msg.GetUsername()); err != nil {
		return nil, adminError(err)
	}
	return connect.NewResponse(&accountv1.RemoveResponse{}), nil
}

func (s *AccountServer) SetAdmin(
	ctx context.Context,
	req *connect.Request[accountv1.SetAdminRequest],
) (*connect.Response[accountv1.SetAdminResponse], error) {
	admins, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := admins.SetAdmin(ctx, req.Msg.GetUsername(), req.Msg.GetAdmin()); err != nil {
		return nil, adminError(err)
	}
	return connect.NewResponse(&accountv1.SetAdminResponse{}), nil
}

// SignIn is refused to anyone but an administrator, like every other call
// here but Me: an issuer or a required group says something about the
// deployment.
func (s *AccountServer) SignIn(
	ctx context.Context,
	_ *connect.Request[accountv1.SignInRequest],
) (*connect.Response[accountv1.SignInResponse], error) {
	admins, err := s.forOwner(ctx)
	if err != nil {
		return nil, err
	}
	me, err := admins.Me(ctx)
	if err != nil {
		return nil, adminError(err)
	}
	if !me.Admin {
		return nil, adminError(admin.ErrNotAdmin)
	}
	out := &accountv1.SignInResponse{
		Settings: make([]*accountv1.Setting, 0, len(s.settings)),
		File:     s.file,
	}
	for _, setting := range s.settings {
		out.Settings = append(out.Settings, &accountv1.Setting{
			Label:  setting.Label,
			Value:  setting.Value,
			Source: setting.Origin.Kind,
			Where:  setting.Origin.Where,
			Key:    setting.Origin.Key,
			Flag:   setting.Flag,
			Env:    setting.Env,
		})
	}
	return connect.NewResponse(out), nil
}

//nolint:ireturn // a factory for a port returns that port; a concrete type here would defeat it.
func (s *AccountServer) forOwner(ctx context.Context) (Admin, error) {
	user, err := owner.From(ctx)
	if err != nil {
		return nil, asConnectError(err)
	}
	admins, err := s.admins(user)
	if err != nil {
		return nil, asConnectError(err)
	}
	return admins, nil
}

// adminCodes are the Connect codes the administrator's refusals answer with.
var adminCodes = map[error]connect.Code{
	admin.ErrNotAdmin:         connect.CodePermissionDenied,
	admin.ErrNoAccount:        connect.CodeNotFound,
	password.ErrNoSuchAccount: connect.CodeNotFound,
	admin.ErrLastAdmin:        connect.CodeFailedPrecondition,
	admin.ErrNoPasswords:      connect.CodeFailedPrecondition,
	admin.ErrSelf:             connect.CodeFailedPrecondition,
	password.ErrTaken:         connect.CodeAlreadyExists,
	password.ErrUsername:      connect.CodeInvalidArgument,
}

func adminError(err error) error {
	for refusal, code := range adminCodes {
		if errors.Is(err, refusal) {
			return connect.NewError(code, err)
		}
	}
	return connect.NewError(connect.CodeInternal, err)
}
