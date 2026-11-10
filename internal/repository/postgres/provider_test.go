package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/account"
	"github.com/bamsammich/docsearch/internal/account/pgaccount"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// ProviderSuite holds groups that follow a provider group, and
// administrators who come from one, to the database that enforces them.
// ana is an administrator; eng and ops follow provider groups of the same
// names, and research follows none.
type ProviderSuite struct {
	suite.Suite
	db       *pgtest.DB
	accounts *pgaccount.Store
}

func TestProvider(t *testing.T) { suite.Run(t, new(ProviderSuite)) }

func (s *ProviderSuite) SetupTest() {
	ctx := s.T().Context()
	s.db = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(ctx, s.db.Owner))
	for _, account := range []string{"ana", "ben", "cy"} {
		_, err := s.db.Owner.ExecContext(
			ctx,
			`SELECT create_account($1, 'https://issuer.example', $1), set_username($1, $1)`,
			account,
		)
		s.Require().NoError(err)
	}
	_, err := s.db.Owner.ExecContext(ctx, `UPDATE users SET admin = true WHERE user_id = 'ana'`)
	s.Require().NoError(err)
	for slug, name := range map[string]string{"eng": "Engineering", "ops": "Operations", "research": "Research"} {
		_, err := s.groups("ana").Create(ctx, slug, name)
		s.Require().NoError(err)
	}
	s.Require().NoError(s.groups("ana").Link(ctx, "eng", "eng"))
	s.Require().NoError(s.groups("ana").Link(ctx, "ops", "ops"))
	s.accounts = pgaccount.New(s.db.App)
}

func (s *ProviderSuite) groups(user string) *postgres.Groups {
	return postgres.NewGroups(s.db.App, user)
}

// sync applies one token's groups to who.
func (s *ProviderSuite) sync(who string, sync account.ProviderSync) {
	s.Require().NoError(s.accounts.Sync(s.T().Context(), who, sync))
}

// member is who's membership of slug: its role and whether the provider
// gave it, or "" for none.
func (s *ProviderSuite) member(slug, who string) (string, bool) {
	members, err := s.groups("ana").Members(s.T().Context(), slug)
	s.Require().NoError(err)
	for _, m := range members {
		if m.Username == who && !m.Invited {
			return m.Role, m.FromProvider
		}
	}
	return "", false
}

func (s *ProviderSuite) admin(who string) bool {
	var admin bool
	s.Require().NoError(s.db.Owner.QueryRowContext(s.T().Context(),
		`SELECT admin FROM users WHERE user_id = $1`, who).Scan(&admin))
	return admin
}

// Membership and the administrator role follow the claim from one token to
// the next.
func (s *ProviderSuite) TestMembershipAndAdminFollowTheClaimAcrossTwoTokens() {
	s.sync("ben", account.ProviderSync{
		Carried: []string{"admins", "eng", "research"}, SetAdmin: true, Admin: true,
	})
	role, fromProvider := s.member("eng", "ben")
	s.Equal(sharing.RoleMember, role)
	s.True(fromProvider)
	role, _ = s.member("research", "ben")
	s.Empty(role, "research follows no provider group")
	s.True(s.admin("ben"))

	s.sync("ben", account.ProviderSync{Carried: []string{"ops"}, SetAdmin: true, Admin: false})
	role, _ = s.member("eng", "ben")
	s.Empty(role, "dropped at the provider, dropped here")
	role, fromProvider = s.member("ops", "ben")
	s.Equal(sharing.RoleMember, role)
	s.True(fromProvider)
	s.False(s.admin("ben"))
}

// A promotion made in docsearch lasts until the account next signs in.
func (s *ProviderSuite) TestAPromotionMadeInDocsearchIsUndoneAtTheNextSignIn() {
	ctx := s.T().Context()
	s.Require().NoError(postgres.NewAdmins(s.db.App, "ana").SetAdmin(ctx, "ben", true))
	s.True(s.admin("ben"))

	s.sync("ben", account.ProviderSync{Carried: []string{"eng"}, SetAdmin: true, Admin: false})
	s.False(s.admin("ben"))
}

// The provider may take the role from the last administrator, which
// set_admin refuses: once an admin group is set, the provider decides.
func (s *ProviderSuite) TestTheProviderMayRemoveTheLastAdministrator() {
	s.sync("ana", account.ProviderSync{SetAdmin: true, Admin: false})
	s.False(s.admin("ana"))
}

// Without an admin group the role is left as docsearch set it.
func (s *ProviderSuite) TestNoAdminGroupLeavesTheRoleAlone() {
	s.sync("ana", account.ProviderSync{Carried: []string{}})
	s.True(s.admin("ana"))
}

// Someone invited by hand stays whatever the provider says, and a pending
// invitation gives way to the provider's membership.
func (s *ProviderSuite) TestInvitedMembersStay() {
	ctx := s.T().Context()
	invited, err := s.groups("ana").Invite(ctx, "eng", "cy", sharing.RoleMember)
	s.Require().NoError(err)
	s.Require().True(invited)
	_, err = s.groups("cy").Accept(ctx, "eng")
	s.Require().NoError(err)

	s.sync("cy", account.ProviderSync{Carried: []string{"eng"}})
	s.sync("cy", account.ProviderSync{Carried: []string{}})
	role, fromProvider := s.member("eng", "cy")
	s.Equal(sharing.RoleMember, role)
	s.False(fromProvider)

	_, err = s.groups("ana").Invite(ctx, "ops", "cy", sharing.RoleMember)
	s.Require().NoError(err)
	s.sync("cy", account.ProviderSync{Carried: []string{"ops"}})
	_, fromProvider = s.member("ops", "cy")
	s.True(fromProvider)
	invitations, err := s.groups("cy").Invitations(ctx)
	s.Require().NoError(err)
	s.Empty(invitations)
}

// A provider membership is removed only by the provider.
func (s *ProviderSuite) TestAProviderMembershipIsNotRemovedByHand() {
	s.sync("ben", account.ProviderSync{Carried: []string{"eng"}})
	err := s.groups("ana").Remove(s.T().Context(), "eng", "ben")
	s.Require().ErrorIs(err, sharing.ErrFromProvider)
}

// A library granted to a linked group reaches its provider members, and
// stops when the provider drops them.
func (s *ProviderSuite) TestALinkedGroupsLibraryFollowsTheProvider() {
	ctx := s.T().Context()
	_, err := postgres.NewLibraries(s.db.App, "ana").Create(ctx, "team", "Team docs")
	s.Require().NoError(err)
	s.Require().NoError(s.groups("ana").GrantLibrary(ctx, "team", "eng", "reader"))

	s.sync("ben", account.ProviderSync{Carried: []string{"eng"}})
	s.Equal("reader", s.roleOn("ben", "team"))
	s.sync("ben", account.ProviderSync{Carried: []string{}})
	s.Empty(s.roleOn("ben", "team"))
}

func (s *ProviderSuite) roleOn(who, slug string) string {
	libs, err := postgres.NewLibraries(s.db.App, who).List(s.T().Context())
	s.Require().NoError(err)
	for _, lib := range libs {
		if lib.Name == slug {
			return lib.Role
		}
	}
	return ""
}

// Only an administrator links, one group follows a provider group, and
// changing the link takes out the members the old one gave.
func (s *ProviderSuite) TestLinking() {
	ctx := s.T().Context()
	s.Require().ErrorIs(s.groups("ben").Link(ctx, "research", "research"), sharing.ErrNoGroup)
	s.Require().ErrorIs(s.groups("ana").Link(ctx, "research", "eng"), sharing.ErrProviderTaken)

	s.sync("ben", account.ProviderSync{Carried: []string{"eng"}})
	s.Require().NoError(s.groups("ana").Link(ctx, "eng", ""))
	role, _ := s.member("eng", "ben")
	s.Empty(role)

	link, err := s.groups("ana").ProviderLink(ctx, "ops")
	s.Require().NoError(err)
	s.Equal(sharing.ProviderLink{ProviderGroup: "ops", Admin: true}, link)
	link, err = s.groups("ben").ProviderLink(ctx, "ops")
	s.Require().NoError(err)
	s.Equal(sharing.ProviderLink{}, link, "only an administrator learns the link")
	_, err = s.groups("ben").ProviderLink(ctx, "nope")
	s.Require().ErrorIs(err, sharing.ErrNoGroup)

	events, err := s.groups("ana").Events(ctx, "eng")
	s.Require().NoError(err)
	var actions []string
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	s.Equal([]string{"create", "link", "provider_join", "provider_leave", "unlink"}, actions)
}

// Auto-creation makes a linked group with no maintainer for each name it
// is handed, and a taken slug gets a number.
func (s *ProviderSuite) TestAutoCreateMakesLinkedGroups() {
	ctx := s.T().Context()
	s.sync("ben", account.ProviderSync{
		Carried: []string{"Research", "Data Science", "eng"},
		Create:  []string{"Research", "Data Science", "eng"},
	})
	all, err := s.groups("ana").All(ctx)
	s.Require().NoError(err)
	var slugs []string
	for _, g := range all {
		slugs = append(slugs, g.Slug)
	}
	s.Equal([]string{"data-science", "eng", "ops", "research", "research-2"}, slugs)

	link, err := s.groups("ana").ProviderLink(ctx, "research-2")
	s.Require().NoError(err)
	s.Equal("Research", link.ProviderGroup)
	members, err := s.groups("ana").Members(ctx, "research-2")
	s.Require().NoError(err)
	s.Equal([]sharing.GroupMember{{Username: "ben", Role: sharing.RoleMember, FromProvider: true}},
		members)
}

// The request role syncs only the account its transaction acts for.
func (s *ProviderSuite) TestAnAccountSyncsOnlyItself() {
	_, err := s.db.App.ExecContext(s.T().Context(),
		`SELECT set_config('app.user_id', 'ben', false),
		        sync_provider_admin('cy', true)`)
	s.Require().ErrorContains(err, "an account names only itself")
	s.False(s.admin("cy"))
}
