package connectapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/api/connectapi"
	"github.com/bamsammich/docsearch/internal/api/connectapi/mocks"
	accountv1 "github.com/bamsammich/docsearch/internal/api/docsearch/account/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/account/v1/accountv1connect"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/service/admin"
)

// AccountAPISuite drives the account handler over HTTP.
type AccountAPISuite struct {
	suite.Suite
	admin  *mocks.MockAdmin
	client accountv1connect.AccountServiceClient
}

func TestAccountAPI(t *testing.T) { suite.Run(t, new(AccountAPISuite)) }

func (s *AccountAPISuite) SetupTest() {
	s.admin = mocks.NewMockAdmin(s.T())
	path, handler := connectapi.NewAccountServer(
		func(string) (connectapi.Admin, error) { return s.admin, nil },
		"password",
		[]connectapi.Setting{
			{
				Label: "Sign-in",
				Value: "Accounts docsearch holds",
				Flag:  "--auth",
				Origin: config.Origin{
					Kind:  config.FromFile,
					Where: "/etc/docsearch.yaml",
					Key:   "auth.mode",
				},
			},
		},
		"/etc/docsearch.yaml",
	)
	mux := http.NewServeMux()
	mux.Handle(path, owner.Middleware(owner.Builtin, handler))
	server := httptest.NewServer(mux)
	s.T().Cleanup(server.Close)
	s.client = accountv1connect.NewAccountServiceClient(server.Client(), server.URL)
}

func (s *AccountAPISuite) TestMeNamesTheModeForAnyone() {
	s.admin.EXPECT().Me(mock.Anything).Return(admin.Me{Username: "ben"}, nil)

	res, err := s.client.Me(s.T().Context(), connect.NewRequest(&accountv1.MeRequest{}))
	s.Require().NoError(err)
	s.Equal("ben", res.Msg.GetUsername())
	s.False(res.Msg.GetAdmin())
	s.Equal("password", res.Msg.GetAuth())
}

func (s *AccountAPISuite) TestTheSignInSettingsAreForAdministratorsOnly() {
	s.admin.EXPECT().Me(mock.Anything).Return(admin.Me{Username: "ben"}, nil).Once()
	_, err := s.client.SignIn(s.T().Context(), connect.NewRequest(&accountv1.SignInRequest{}))
	s.Equal(connect.CodePermissionDenied, connect.CodeOf(err))

	s.admin.EXPECT().Me(mock.Anything).Return(admin.Me{Username: "ana", Admin: true}, nil).Once()
	res, err := s.client.SignIn(s.T().Context(), connect.NewRequest(&accountv1.SignInRequest{}))
	s.Require().NoError(err)
	s.Require().Len(res.Msg.GetSettings(), 1)
	got := res.Msg.GetSettings()[0]
	s.Equal("--auth", got.GetFlag())
	s.Equal("file", got.GetSource())
	s.Equal("auth.mode", got.GetKey())
	s.Equal("/etc/docsearch.yaml", res.Msg.GetFile())
}

func (s *AccountAPISuite) TestEachRefusalAnswersWithItsCode() {
	for refusal, code := range map[error]connect.Code{
		admin.ErrNotAdmin:    connect.CodePermissionDenied,
		admin.ErrLastAdmin:   connect.CodeFailedPrecondition,
		admin.ErrNoPasswords: connect.CodeFailedPrecondition,
		password.ErrTaken:    connect.CodeAlreadyExists,
		password.ErrUsername: connect.CodeInvalidArgument,
	} {
		s.admin.EXPECT().Add(mock.Anything, "cy").Return("", refusal).Once()
		_, err := s.client.Add(
			s.T().Context(),
			connect.NewRequest(&accountv1.AddRequest{Username: "cy"}),
		)
		s.Equal(code, connect.CodeOf(err), refusal.Error())
	}
}
