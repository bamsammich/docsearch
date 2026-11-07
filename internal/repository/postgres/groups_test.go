package postgres_test

import (
	"database/sql"
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

// A group gets reader or editor, never admin: administering a library
// takes your own grant, so a group member can't make themselves its admin
// and push out the admins it has.
func (s *GroupsSuite) TestAGroupNeverAdministersALibrary() {
	ctx := s.T().Context()
	s.Require().ErrorIs(s.groups("ana").GrantLibrary(ctx, "team", "eng", sharing.Admin),
		sharing.ErrGroupRole)
	s.Require().NoError(s.groups("ana").GrantLibrary(ctx, "team", "eng", sharing.Editor))

	ben := postgres.NewLibraries(s.db.App, "ben")
	_, err := ben.Grant(ctx, "team", "ben", sharing.Admin)
	s.Require().ErrorIs(err, sharing.ErrNoLibrary, "an editor through a group grants nothing")
	s.Require().ErrorIs(ben.Revoke(ctx, "team", "ana"), sharing.ErrNoLibrary)
	_, err = ben.Rename(ctx, "team", "Ben's now")
	s.Require().ErrorIs(err, sharing.ErrNoLibrary)
	_, err = ben.Delete(ctx, "team")
	s.Require().ErrorIs(err, sharing.ErrNoLibrary)
	s.Equal(sharing.Admin, s.roleOn("ana", "team"))
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
	s.Equal("group_delete", last.Action)
	s.Equal("Engineering", last.Detail, "the log keeps the name the group had")
}

// A membership the provider gives is taken away only at the provider.
func (s *GroupsSuite) TestAProviderMembershipIsNotRemovedByHand() {
	ctx := s.T().Context()
	_, err := s.db.Owner.ExecContext(ctx, `
		INSERT INTO group_members (group_id, user_id, role, source)
		SELECT group_id, 'dee', 'member', 'provider' FROM groups WHERE slug = 'eng'`)
	s.Require().NoError(err)
	s.Require().ErrorIs(s.groups("ana").Remove(ctx, "eng", "dee"), sharing.ErrFromProvider)

	invited, err := s.groups("ana").Invite(ctx, "eng", "dee", sharing.RoleMaintainer)
	s.Require().NoError(err)
	s.False(invited)
	s.Require().ErrorIs(s.groups("ana").Remove(ctx, "eng", "dee"), sharing.ErrFromProvider,
		"a role change keeps the membership the provider's")
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

// An administrator manages any group, but joins one only by force, and the
// group and each of its libraries record the forced addition.
func (s *GroupsSuite) TestAnAdministratorJoinsOnlyByForce() {
	ctx := s.T().Context()
	_, err := s.db.Owner.ExecContext(ctx, `UPDATE users SET admin = true WHERE user_id = 'dee'`)
	s.Require().NoError(err)
	s.Require().NoError(s.groups("ana").GrantLibrary(ctx, "team", "eng", sharing.Reader))

	_, err = s.groups("dee").Invite(ctx, "eng", "dee", sharing.RoleMaintainer)
	s.Require().ErrorIs(err, sharing.ErrJoinByForce)
	invited, err := s.groups("dee").Invite(ctx, "eng", "cy", sharing.RoleMember)
	s.Require().NoError(err)
	s.True(invited, "an administrator invites others through the usual invitation")
	s.Empty(s.roleOn("cy", "team"))

	s.Require().ErrorIs(s.groups("ana").Force(ctx, "eng", "cy", sharing.RoleMember),
		sharing.ErrNoGroup, "only an administrator adds by force")
	s.Require().NoError(s.groups("dee").Force(ctx, "eng", "dee", sharing.RoleMember))
	s.Equal(sharing.Reader, s.roleOn("dee", "team"))

	groupLog, err := s.groups("ana").Events(ctx, "eng")
	s.Require().NoError(err)
	forced := groupLog[len(groupLog)-1]
	s.Equal("force", forced.Action)
	s.Equal("dee", forced.Actor)
	s.Equal("dee", forced.Subject)
	libraryLog, err := postgres.NewLibraries(s.db.App, "ana").Events(ctx, "team")
	s.Require().NoError(err)
	reached := libraryLog[len(libraryLog)-1]
	s.Equal("group_force", reached.Action)
	s.Equal("dee", reached.Subject)
	s.Equal("Engineering", reached.Detail)
}

// Two maintainers stepping down at once leave the group one of them: the
// second waits for the first and is refused.
func (s *GroupsSuite) TestTwoMaintainersCannotBothStepDown() {
	ctx := s.T().Context()
	_, err := s.groups("ana").Invite(ctx, "eng", "ben", sharing.RoleMaintainer)
	s.Require().NoError(err)
	as := func(user string) *sql.Tx {
		tx, err := s.db.App.BeginTx(ctx, nil)
		s.Require().NoError(err)
		_, err = tx.ExecContext(ctx, `SELECT set_config('app.user_id', $1, true)`, user)
		s.Require().NoError(err)
		return tx
	}
	first, second := as("ana"), as("ben")
	_, err = first.ExecContext(ctx, `SELECT remove_from_group('eng', 'ana')`)
	s.Require().NoError(err)
	refused := make(chan error, 1)
	go func() {
		_, err := second.ExecContext(ctx, `SELECT remove_from_group('eng', 'ben')`)
		refused <- err
	}()
	s.Require().NoError(first.Commit())
	s.Require().ErrorContains(<-refused, "without a maintainer")
	s.Require().NoError(second.Rollback())
	members, err := s.groups("ben").Members(ctx, "eng")
	s.Require().NoError(err)
	s.Equal([]sharing.GroupMember{{Username: "ben", Role: sharing.RoleMaintainer}}, members)
}

// A group name holds no control characters, which a terminal would obey.
func (s *GroupsSuite) TestAGroupNameHoldsNoControlCharacters() {
	ctx := s.T().Context()
	_, err := s.groups("cy").Create(ctx, "ops", "Ops\x1b]8;;https://example.com\x07")
	s.Require().ErrorIs(err, sharing.ErrGroupName)
	s.Require().ErrorIs(s.groups("ana").Rename(ctx, "eng", "Eng\nineering"), sharing.ErrGroupName)
}
