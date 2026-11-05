package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/schema"
	adminservice "github.com/bamsammich/docsearch/internal/service/admin"
)

// AdminsSuite checks the administrator's definer functions against
// Postgres: ana is made an administrator by the owner, as the CLI does, and
// ben and cy are not.
type AdminsSuite struct {
	suite.Suite
	db *pgtest.DB
}

func TestAdmins(t *testing.T) { suite.Run(t, new(AdminsSuite)) }

func (s *AdminsSuite) SetupTest() {
	s.db = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.db.Owner))
	for _, account := range []string{"ana", "ben", "cy"} {
		_, err := s.db.Owner.ExecContext(
			s.T().Context(),
			`SELECT create_account($1, 'https://issuer.example', $1), set_username($1, $1)`,
			account,
		)
		s.Require().NoError(err)
	}
	found, err := postgres.GrantAdmin(s.T().Context(), s.db.Owner, "ana", true)
	s.Require().NoError(err)
	s.Require().True(found)
}

func (s *AdminsSuite) as(user string) *postgres.Admins {
	return postgres.NewAdmins(s.db.App, user)
}

func (s *AdminsSuite) TestAnAccountKnowsItsOwnNameAndWhetherItAdministers() {
	me, err := s.as("ana").Me(s.T().Context())
	s.Require().NoError(err)
	s.Equal(adminservice.Me{Username: "ana", Admin: true}, me)

	me, err = s.as("ben").Me(s.T().Context())
	s.Require().NoError(err)
	s.Equal(adminservice.Me{Username: "ben"}, me)
}

// The built-in user nobody signs in as is not an account to manage.
func (s *AdminsSuite) TestTheListLeavesOutTheBuiltInUser() {
	accounts, err := s.as("ana").List(s.T().Context())
	s.Require().NoError(err)

	names := make([]string, 0, len(accounts))
	for _, a := range accounts {
		names = append(names, a.Username)
	}
	s.Equal([]string{"ana", "ben", "cy"}, names)
	s.True(accounts[0].Admin)
}

func (s *AdminsSuite) TestOnlyAnAdministratorMakesAnother() {
	s.Require().
		ErrorIs(s.as("ben").SetAdmin(s.T().Context(), "ben", true), adminservice.ErrNotAdmin)

	s.Require().NoError(s.as("ana").SetAdmin(s.T().Context(), "ben", true))
	me, err := s.as("ben").Me(s.T().Context())
	s.Require().NoError(err)
	s.True(me.Admin)
}

func (s *AdminsSuite) TestTheLastAdministratorStaysOne() {
	s.Require().
		ErrorIs(s.as("ana").SetAdmin(s.T().Context(), "ana", false), adminservice.ErrLastAdmin)

	s.Require().NoError(s.as("ana").SetAdmin(s.T().Context(), "ben", true))
	s.Require().NoError(s.as("ana").SetAdmin(s.T().Context(), "ana", false))
}

func (s *AdminsSuite) TestAnUnknownAccountIsNamedAsSuch() {
	s.Require().
		ErrorIs(s.as("ana").SetAdmin(s.T().Context(), "zed", true), adminservice.ErrNoAccount)
}

// Every change is recorded, and only an administrator can write the record.
func (s *AdminsSuite) TestChangesAreRecordedAndOnlyByAnAdministrator() {
	s.Require().NoError(s.as("ana").SetAdmin(s.T().Context(), "ben", true))
	s.Require().NoError(s.as("ana").Record(s.T().Context(), adminservice.ActionReset, "cy"))
	s.Require().
		ErrorIs(s.as("cy").Record(s.T().Context(), adminservice.ActionReset, "cy"), adminservice.ErrNotAdmin)

	var actions []string
	rows, err := s.db.Owner.QueryContext(s.T().Context(), `
		SELECT a.username || ' ' || e.action || ' ' || s.username
		  FROM account_events e JOIN users a ON a.user_id = e.actor JOIN users s ON s.user_id = e.subject
		 ORDER BY e.id`)
	s.Require().NoError(err)
	defer func() { s.NoError(rows.Close()) }()
	for rows.Next() {
		var line string
		s.Require().NoError(rows.Scan(&line))
		actions = append(actions, line)
	}
	s.Require().NoError(rows.Err())
	s.Equal([]string{"ana admin ben", "ana reset cy"}, actions)
}

func (s *AdminsSuite) TestTheOwnerCannotMakeAnAccountThatDoesNotExistAnAdministrator() {
	found, err := postgres.GrantAdmin(s.T().Context(), s.db.Owner, "zed", true)
	s.Require().NoError(err)
	s.False(found)
}
