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
	"github.com/bamsammich/docsearch/internal/service/settings"
)

// AccountAPISuite drives the account handler over HTTP.
type AccountAPISuite struct {
	suite.Suite
	admin    *mocks.MockAdmin
	settings *mocks.MockSettings
	client   accountv1connect.AccountServiceClient
}

func TestAccountAPI(t *testing.T) { suite.Run(t, new(AccountAPISuite)) }

func (s *AccountAPISuite) SetupTest() {
	s.admin = mocks.NewMockAdmin(s.T())
	s.settings = mocks.NewMockSettings(s.T())
	path, handler := connectapi.NewAccountServer(
		func(string) (connectapi.Admin, error) { return s.admin, nil },
		func(string) (connectapi.Settings, error) { return s.settings, nil },
		"password",
		[]connectapi.Setting{
			{
				Label: "Sign-in",
				Value: "Accounts docsearch holds",
				Name:  "auth",
				Flag:  "--auth",
				Origin: config.Origin{
					Kind:  config.FromFile,
					Where: "/etc/docsearch.yaml",
					Key:   "auth.mode",
				},
			},
			{
				Label:    "Sign-in page",
				Value:    "https://docs.example.com",
				Name:     "public-url",
				Editable: true,
				Origin:   config.Origin{Kind: config.FromEnv, Where: config.EnvPublicURL},
			},
			{
				Label:    "New people",
				Value:    "false",
				Name:     "oidc-sign-up",
				Editable: true,
				Boolean:  true,
				Origin:   config.Origin{Kind: config.FromDefault},
			},
		},
		"/etc/docsearch.yaml",
		"",
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
	s.settings.EXPECT().Stored(mock.Anything).Return(map[string]string{}, nil)
	res, err := s.client.SignIn(s.T().Context(), connect.NewRequest(&accountv1.SignInRequest{}))
	s.Require().NoError(err)
	s.Require().Len(res.Msg.GetSettings(), 3)
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
		password.Stranded{Detail: "removing cy would leave Team docs without an admin"}: connect.CodeFailedPrecondition,
	} {
		s.admin.EXPECT().Add(mock.Anything, "cy").Return("", refusal).Once()
		_, err := s.client.Add(
			s.T().Context(),
			connect.NewRequest(&accountv1.AddRequest{Username: "cy"}),
		)
		s.Equal(code, connect.CodeOf(err), refusal.Error())
	}
}

// What the environment sets cannot change from the UI, so it is not offered,
// and a kept value that has not taken effect yet is shown beside the running
// one.
func (s *AccountAPISuite) TestASettingTheEnvironmentSetsIsNotOfferedForEditing() {
	s.admin.EXPECT().Me(mock.Anything).Return(admin.Me{Username: "ana", Admin: true}, nil)
	s.settings.EXPECT().Stored(mock.Anything).Return(map[string]string{"oidc-sign-up": "true"}, nil)

	res, err := s.client.SignIn(s.T().Context(), connect.NewRequest(&accountv1.SignInRequest{}))
	s.Require().NoError(err)

	page, people := res.Msg.GetSettings()[1], res.Msg.GetSettings()[2]
	s.False(page.GetEditable())
	s.True(people.GetEditable())
	s.True(people.GetBoolean())
	s.True(people.GetHasStored())
	s.Equal("true", people.GetStored())
}

func (s *AccountAPISuite) TestAnUpdateReturnsTheFileWithItsChangedLines() {
	s.admin.EXPECT().Me(mock.Anything).Return(admin.Me{Username: "ana", Admin: true}, nil)
	s.settings.EXPECT().
		Update(mock.Anything, []settings.Change{{Flag: "oidc-sign-up", Value: "true"}}).
		Return(settings.Result{File: "auth:\n  oidc:\n    sign_up: true\n", Changed: []int{3}}, nil)

	res, err := s.client.UpdateSignIn(
		s.T().Context(),
		connect.NewRequest(&accountv1.UpdateSignInRequest{
			Changes: []*accountv1.Change{{Name: "oidc-sign-up", Value: "true"}},
		}),
	)
	s.Require().NoError(err)
	s.Equal([]int32{3}, res.Msg.GetChangedLines())
	s.False(res.Msg.GetSaved())
}

func (s *AccountAPISuite) TestARefusedUpdateSaysWhy() {
	s.admin.EXPECT().Me(mock.Anything).Return(admin.Me{Username: "ana", Admin: true}, nil)
	s.settings.EXPECT().
		Update(mock.Anything, mock.Anything).
		Return(settings.Result{}, settings.ErrLocked)

	_, err := s.client.UpdateSignIn(
		s.T().Context(),
		connect.NewRequest(&accountv1.UpdateSignInRequest{
			Changes: []*accountv1.Change{{Name: "public-url", Value: "https://x.example.com"}},
		}),
	)
	s.Equal(connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func (s *AccountAPISuite) TestOnlyAnAdministratorUpdates() {
	s.admin.EXPECT().Me(mock.Anything).Return(admin.Me{Username: "ben"}, nil)

	_, err := s.client.UpdateSignIn(
		s.T().Context(),
		connect.NewRequest(&accountv1.UpdateSignInRequest{}),
	)
	s.Equal(connect.CodePermissionDenied, connect.CodeOf(err))
}
