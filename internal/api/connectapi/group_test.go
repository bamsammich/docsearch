package connectapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/api/connectapi"
	"github.com/bamsammich/docsearch/internal/api/connectapi/mocks"
	groupv1 "github.com/bamsammich/docsearch/internal/api/docsearch/group/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/group/v1/groupv1connect"
	libraryv1 "github.com/bamsammich/docsearch/internal/api/docsearch/library/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/library/v1/libraryv1connect"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// GroupAPISuite drives the group handler, and the library handler's group
// grants, over HTTP.
type GroupAPISuite struct {
	suite.Suite
	groups    *mocks.MockGroups
	client    groupv1connect.GroupServiceClient
	libraries libraryv1connect.LibraryServiceClient
	mayCreate bool
}

func TestGroupAPI(t *testing.T) { suite.Run(t, new(GroupAPISuite)) }

func (s *GroupAPISuite) SetupTest() {
	s.groups = mocks.NewMockGroups(s.T())
	s.mayCreate = true
	groupsFor := func(string) (connectapi.Groups, error) { return s.groups, nil }
	groupPath, groupHandler := connectapi.NewGroupServer(groupsFor,
		func(context.Context, string) (bool, error) { return s.mayCreate, nil })
	libraryPath, libraryHandler := connectapi.NewLibraryServer(
		func(string) (connectapi.Libraries, error) { return nil, nil }, groupsFor)
	mux := http.NewServeMux()
	mux.Handle(groupPath, owner.Middleware(owner.Builtin, groupHandler))
	mux.Handle(libraryPath, owner.Middleware(owner.Builtin, libraryHandler))
	server := httptest.NewServer(mux)
	s.T().Cleanup(server.Close)
	s.client = groupv1connect.NewGroupServiceClient(server.Client(), server.URL)
	s.libraries = libraryv1connect.NewLibraryServiceClient(server.Client(), server.URL)
}

func (s *GroupAPISuite) TestAGroupIsCreatedWhereTheDeploymentAllows() {
	s.groups.EXPECT().Create(mock.Anything, "eng", "Engineering").
		Return(sharing.Group{Slug: "eng", Name: "Engineering", Role: sharing.RoleMaintainer, Members: 1}, nil)

	res, err := s.client.Create(s.T().Context(),
		connect.NewRequest(&groupv1.CreateRequest{Group: "eng", Name: "Engineering"}))
	s.Require().NoError(err)
	s.Equal(sharing.RoleMaintainer, res.Msg.GetGroup().GetRole())
}

func (s *GroupAPISuite) TestOnlyAdministratorsCreateWhereTheDeploymentSaysSo() {
	s.mayCreate = false

	_, err := s.client.Create(s.T().Context(),
		connect.NewRequest(&groupv1.CreateRequest{Group: "eng", Name: "Engineering"}))
	s.Equal(connect.CodePermissionDenied, connect.CodeOf(err))
	s.groups.AssertNotCalled(s.T(), "Create", mock.Anything, mock.Anything, mock.Anything)
}

func (s *GroupAPISuite) TestTheListSaysWhetherTheCallerMayCreate() {
	s.mayCreate = false
	s.groups.EXPECT().All(mock.Anything).
		Return([]sharing.Group{{Slug: "eng", Name: "Engineering", Members: 3}}, nil)

	res, err := s.client.List(s.T().Context(), connect.NewRequest(&groupv1.ListRequest{}))
	s.Require().NoError(err)
	s.False(res.Msg.GetCanCreate())
	s.Require().Len(res.Msg.GetGroups(), 1)
	s.Equal(int64(3), res.Msg.GetGroups()[0].GetMembers())
}

func (s *GroupAPISuite) TestEachRefusalAnswersWithItsCode() {
	for refusal, want := range map[error]connect.Code{
		sharing.ErrNoGroup:           connect.CodeNotFound,
		sharing.ErrLastMaintainer:    connect.CodeFailedPrecondition,
		sharing.ErrFromProvider:      connect.CodeFailedPrecondition,
		sharing.ErrNoGroupInvitation: connect.CodeNotFound,
		sharing.ErrJoinByForce:       connect.CodeFailedPrecondition,
		sharing.ErrGroupName:         connect.CodeInvalidArgument,
		sharing.ErrGroupRole:         connect.CodeInvalidArgument,
	} {
		s.SetupTest()
		s.groups.EXPECT().Remove(mock.Anything, "eng", "ben").Return(refusal)
		_, err := s.client.Remove(s.T().Context(),
			connect.NewRequest(&groupv1.RemoveRequest{Group: "eng", Username: "ben"}))
		s.Equal(want, connect.CodeOf(err), refusal.Error())
	}
}

func (s *GroupAPISuite) TestALibraryIsGrantedToAGroup() {
	s.groups.EXPECT().GrantLibrary(mock.Anything, "team", "eng", sharing.Editor).Return(nil)
	s.groups.EXPECT().OnLibrary(mock.Anything, "team").
		Return([]sharing.GroupGrant{{Group: "eng", Name: "Engineering", Role: sharing.Editor, Members: 3}}, nil)

	_, err := s.libraries.GrantGroup(s.T().Context(), connect.NewRequest(
		&libraryv1.GrantGroupRequest{Library: "team", Group: "eng", Role: sharing.Editor}))
	s.Require().NoError(err)
	res, err := s.libraries.Groups(s.T().Context(),
		connect.NewRequest(&libraryv1.GroupsRequest{Library: "team"}))
	s.Require().NoError(err)
	s.Require().Len(res.Msg.GetGroups(), 1)
	s.Equal("eng", res.Msg.GetGroups()[0].GetGroup())
}

// Invite with force adds the person at once through Force, never through an
// invitation.
func (s *GroupAPISuite) TestForceAddsWithNoInvitation() {
	s.groups.EXPECT().Force(mock.Anything, "eng", "dee", sharing.RoleMember).Return(nil)

	res, err := s.client.Invite(s.T().Context(), connect.NewRequest(&groupv1.InviteRequest{
		Group: "eng", Username: "dee", Role: sharing.RoleMember, Force: true,
	}))
	s.Require().NoError(err)
	s.False(res.Msg.GetInvited())
	s.groups.AssertNotCalled(
		s.T(),
		"Invite",
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
	)
}
