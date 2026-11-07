package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// GroupsSuite holds the rules for groups to the database that enforces
// them: who joins, who grants a library to a group, and that a library
// reached through a group follows membership.
type GroupsSuite struct {
	suite.Suite
	db *pgtest.DB
}

func TestGroups(t *testing.T) { suite.Run(t, new(GroupsSuite)) }

func (s *GroupsSuite) SetupTest() {
	s.db = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.db.Owner))
	for _, account := range []string{"ana", "ben", "cy", "dee"} {
		_, err := s.db.Owner.ExecContext(
			s.T().Context(),
			`SELECT create_account($1, 'https://issuer.example', $1), set_username($1, $1)`,
			account,
		)
		s.Require().NoError(err)
	}
	// ana has the team library; ana maintains eng, and ben is in it.
	_, err := postgres.NewLibraries(s.db.App, "ana").Create(s.T().Context(), "team", "Team docs")
	s.Require().NoError(err)
	_, err = s.groups("ana").Create(s.T().Context(), "eng", "Engineering")
	s.Require().NoError(err)
	s.join("eng", "ben", sharing.RoleMember)
}

func (s *GroupsSuite) groups(user string) *postgres.Groups {
	return postgres.NewGroups(s.db.App, user)
}

// join has ana invite who to slug with role, and who accept.
func (s *GroupsSuite) join(slug, who, role string) {
	invited, err := s.groups("ana").Invite(s.T().Context(), slug, who, role)
	s.Require().NoError(err)
	s.Require().True(invited)
	_, err = s.groups(who).Accept(s.T().Context(), slug)
	s.Require().NoError(err)
}

// roleOn is who's role on the library slug names, or "" for none.
func (s *GroupsSuite) roleOn(who, slug string) string {
	libs, err := postgres.NewLibraries(s.db.App, who).List(s.T().Context())
	s.Require().NoError(err)
	for _, lib := range libs {
		if lib.Name == slug {
			return lib.Role
		}
	}
	return ""
}

func (s *GroupsSuite) teamID() string {
	var id string
	s.Require().NoError(s.db.Owner.QueryRowContext(s.T().Context(),
		`SELECT library_id FROM libraries WHERE slug = 'team'`).Scan(&id))
	return id
}

// Everyone signed in sees a group's name; only its members see who is in it.
func (s *GroupsSuite) TestAGroupsNameIsSeenByAllAndItsMembersByMembers() {
	ctx := s.T().Context()
	all, err := s.groups("cy").All(ctx)
	s.Require().NoError(err)
	s.Equal([]sharing.Group{{Slug: "eng", Name: "Engineering", Members: 2}}, all)

	_, err = s.groups("cy").Members(ctx, "eng")
	s.Require().ErrorIs(err, sharing.ErrNoGroup)
	members, err := s.groups("ben").Members(ctx, "eng")
	s.Require().NoError(err)
	s.Equal([]sharing.GroupMember{
		{Username: "ana", Role: sharing.RoleMaintainer},
		{Username: "ben", Role: sharing.RoleMember},
	}, members)
}

// A library granted to a group reaches its members at the group's role,
// and leaves them when they leave. An invitation not yet accepted gives
// nothing.
func (s *GroupsSuite) TestALibraryGrantedToAGroupFollowsMembership() {
	ctx := s.T().Context()
	_, err := s.groups("ana").Invite(ctx, "eng", "cy", sharing.RoleMember)
	s.Require().NoError(err)
	s.Empty(s.roleOn("ben", "team"))

	s.Require().NoError(s.groups("ana").GrantLibrary(ctx, "team", "eng", sharing.Reader))
	s.Equal(sharing.Reader, s.roleOn("ben", "team"))
	s.Empty(s.roleOn("cy", "team"), "an invitation is no membership")

	jobs := postgres.NewJobs(s.db.App, pgsession.Scope{User: "ben", Library: s.teamID()})
	_, _, err = jobs.Add(ctx, "https://example.com/docs", "", false)
	s.Require().Error(err, "a reader through a group adds nothing")
	s.Require().NoError(s.groups("ana").GrantLibrary(ctx, "team", "eng", sharing.Editor))
	_, _, err = jobs.Add(ctx, "https://example.com/docs", "", false)
	s.Require().NoError(err, "an editor through a group adds")

	s.Require().NoError(s.groups("ben").Remove(ctx, "eng", "ben"))
	s.Empty(s.roleOn("ben", "team"), "leaving the group leaves its libraries")
}

// A person's role is the highest of their own grant and their groups'.
func (s *GroupsSuite) TestTheHighestRoleWins() {
	ctx := s.T().Context()
	s.Require().NoError(s.groups("ana").GrantLibrary(ctx, "team", "eng", sharing.Editor))
	invited, err := postgres.NewLibraries(s.db.App, "ana").Grant(ctx, "team", "ben", sharing.Reader)
	s.Require().NoError(err)
	s.Require().True(invited)
	_, err = postgres.NewLibraries(s.db.App, "ben").Accept(ctx, "team")
	s.Require().NoError(err)
	s.Equal(sharing.Editor, s.roleOn("ben", "team"))
}

// Only a library admin who maintains the group, or an administrator, grants
// a library to a group.
func (s *GroupsSuite) TestOnlyAMaintainerWhoAdministersTheLibraryGrants() {
	ctx := s.T().Context()
	_, err := postgres.NewLibraries(s.db.App, "ben").Create(ctx, "bens", "Ben's docs")
	s.Require().NoError(err)
	s.Require().ErrorIs(s.groups("ben").GrantLibrary(ctx, "bens", "eng", sharing.Reader),
		sharing.ErrNoGroup, "ben is in eng but does not maintain it")

	_, err = s.groups("cy").Create(ctx, "ops", "Operations")
	s.Require().NoError(err)
	s.Require().ErrorIs(s.groups("cy").GrantLibrary(ctx, "team", "ops", sharing.Reader),
		sharing.ErrNoLibrary, "cy maintains ops but does not administer team")

	_, err = s.db.Owner.ExecContext(ctx, `UPDATE users SET admin = true WHERE user_id = 'ben'`)
	s.Require().NoError(err)
	s.Require().NoError(s.groups("ben").GrantLibrary(ctx, "bens", "ops", sharing.Reader),
		"an administrator who administers the library grants it to any group")
}

// A group always keeps a maintainer.
func (s *GroupsSuite) TestAGroupKeepsAMaintainer() {
	ctx := s.T().Context()
	s.Require().ErrorIs(s.groups("ana").Remove(ctx, "eng", "ana"), sharing.ErrLastMaintainer)
	_, err := s.groups("ana").Invite(ctx, "eng", "ana", sharing.RoleMember)
	s.Require().ErrorIs(err, sharing.ErrLastMaintainer)

	invited, err := s.groups("ana").Invite(ctx, "eng", "ben", sharing.RoleMaintainer)
	s.Require().NoError(err)
	s.False(invited, "ben is a member, so his role changes")
	s.Require().NoError(s.groups("ana").Remove(ctx, "eng", "ana"),
		"with a second maintainer, ana leaves")
}

// An admin grant to a group never counts as a library's last admin.
func (s *GroupsSuite) TestALibraryKeepsADirectAdmin() {
	ctx := s.T().Context()
	s.Require().NoError(s.groups("ana").GrantLibrary(ctx, "team", "eng", sharing.Admin))
	s.Equal(sharing.Admin, s.roleOn("ben", "team"))
	s.Require().ErrorIs(postgres.NewLibraries(s.db.App, "ana").Revoke(ctx, "team", "ana"),
		sharing.ErrLastAdmin)
}

// Deleting a group takes its grants away and leaves its libraries.
func (s *GroupsSuite) TestDeletingAGroupLeavesItsLibraries() {
	ctx := s.T().Context()
	s.Require().NoError(s.groups("ana").GrantLibrary(ctx, "team", "eng", sharing.Reader))
	s.Require().ErrorIs(s.groups("ben").Delete(ctx, "eng"), sharing.ErrNoGroup)
	s.Require().NoError(s.groups("ana").Delete(ctx, "eng"))

	s.Empty(s.roleOn("ben", "team"))
	s.Equal(sharing.Admin, s.roleOn("ana", "team"))
	events, err := postgres.NewLibraries(s.db.App, "ana").Events(ctx, "team")
	s.Require().NoError(err)
	last := events[len(events)-1]
	s.Equal("group_revoke", last.Action)
	s.Equal("eng", last.Detail)
}

// A membership the provider gives is taken away only at the provider.
func (s *GroupsSuite) TestAProviderMembershipIsNotRemovedByHand() {
	ctx := s.T().Context()
	_, err := s.db.Owner.ExecContext(ctx, `
		INSERT INTO group_members (group_id, user_id, role, source)
		SELECT group_id, 'dee', 'member', 'provider' FROM groups WHERE slug = 'eng'`)
	s.Require().NoError(err)
	s.Require().ErrorIs(s.groups("ana").Remove(ctx, "eng", "dee"), sharing.ErrFromProvider)
}

// A group's maintainers see its invitations and its log; the invitee
// declines, and the name is taken once.
func (s *GroupsSuite) TestInvitationsAreSeenByMaintainersAndDeclined() {
	ctx := s.T().Context()
	_, err := s.groups("ana").Invite(ctx, "eng", "cy", sharing.RoleMember)
	s.Require().NoError(err)
	members, err := s.groups("ana").Members(ctx, "eng")
	s.Require().NoError(err)
	s.Contains(members,
		sharing.GroupMember{Username: "cy", Role: sharing.RoleMember, Invited: true})
	members, err = s.groups("ben").Members(ctx, "eng")
	s.Require().NoError(err)
	s.Len(members, 2, "a member does not see invitations")

	invitations, err := s.groups("cy").Invitations(ctx)
	s.Require().NoError(err)
	s.Require().Len(invitations, 1)
	s.Equal("ana", invitations[0].InvitedBy)
	s.Require().NoError(s.groups("cy").Decline(ctx, "eng"))
	_, err = s.groups("cy").Accept(ctx, "eng")
	s.Require().ErrorIs(err, sharing.ErrNoGroupInvitation)

	_, err = s.groups("cy").Create(ctx, "eng", "Another")
	s.Require().ErrorIs(err, sharing.ErrGroupTaken)
	_, err = s.groups("ben").Events(ctx, "eng")
	s.Require().ErrorIs(err, sharing.ErrNoGroup, "the log is for maintainers")
	events, err := s.groups("ana").Events(ctx, "eng")
	s.Require().NoError(err)
	actions := make([]string, 0, len(events))
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	s.Equal([]string{"create", "invite", "accept", "invite", "decline"}, actions)
}

// The worker writes as a job's requester, so a job queued through a group
// fails once the requester leaves the group.
func (s *GroupsSuite) TestAJobQueuedThroughAGroupFailsWhenItsRequesterLeaves() {
	ctx := s.T().Context()
	s.Require().NoError(s.groups("ana").GrantLibrary(ctx, "team", "eng", sharing.Editor))
	id, _, err := postgres.NewJobs(s.db.App, pgsession.Scope{User: "ben", Library: s.teamID()}).
		Add(ctx, "https://example.com/docs", "", false)
	s.Require().NoError(err)

	queues, err := postgres.NewQueues(s.db.App).Waiting(ctx)
	s.Require().NoError(err)
	s.Len(queues, 1)

	s.Require().NoError(s.groups("ben").Remove(ctx, "eng", "ben"))
	queues, err = postgres.NewQueues(s.db.App).Waiting(ctx)
	s.Require().NoError(err)
	s.Empty(queues)
	var status string
	s.Require().NoError(s.db.Owner.QueryRowContext(ctx,
		`SELECT status FROM ingest_jobs WHERE id = $1`, id).Scan(&status))
	s.Equal("failed", status)
}
