package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/password/pgpassword"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

const firstSecret = "correct horse battery staple"

// FirstAdminSuite creates the administrator a deployment names in its
// environment, against a real database, as the owner migrate connects as.
type FirstAdminSuite struct {
	suite.Suite
	pg *pgtest.DB
}

func TestFirstAdmin(t *testing.T) { suite.Run(t, new(FirstAdminSuite)) }

func (s *FirstAdminSuite) SetupTest() {
	s.pg = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.pg.Owner))
}

func (s *FirstAdminSuite) isAdmin(username string) bool {
	var admin bool
	s.Require().NoError(s.pg.Owner.QueryRowContext(s.T().Context(),
		`SELECT admin FROM users WHERE username = $1`, username).Scan(&admin))
	return admin
}

// The account is created, made an administrator, and signs in with the
// password the environment gave.
func (s *FirstAdminSuite) TestTheNamedAccountIsCreatedAsAnAdministrator() {
	ctx := s.T().Context()
	created, err := firstAdmin{Username: "alice", Secret: firstSecret}.Create(ctx, s.pg.Owner)
	s.Require().NoError(err)
	s.True(created)
	s.True(s.isAdmin("alice"))

	accounts, err := password.New(pgpassword.New(s.pg.App), time.Now)
	s.Require().NoError(err)
	_, err = accounts.Authenticate(ctx, "alice", firstSecret)
	s.Require().NoError(err)
}

// Every start runs migrate, so an account that already exists keeps the
// password and the role it has now, even one an administrator took away.
func (s *FirstAdminSuite) TestAnExistingAccountIsLeftAsItIs() {
	ctx := s.T().Context()
	first := firstAdmin{Username: "alice", Secret: firstSecret}
	_, err := first.Create(ctx, s.pg.Owner)
	s.Require().NoError(err)
	_, err = s.pg.Owner.ExecContext(ctx, `UPDATE users SET admin = false WHERE username = 'alice'`)
	s.Require().NoError(err)

	created, err := firstAdmin{Username: "alice", Secret: "a different password"}.
		Create(ctx, s.pg.Owner)
	s.Require().NoError(err)
	s.False(created)
	s.False(s.isAdmin("alice"))

	accounts, err := password.New(pgpassword.New(s.pg.App), time.Now)
	s.Require().NoError(err)
	_, err = accounts.Authenticate(ctx, "alice", firstSecret)
	s.Require().NoError(err, "the first password still signs in")
}

func (s *FirstAdminSuite) TestAShortPasswordIsRefused() {
	_, err := firstAdmin{Username: "alice", Secret: "short"}.Create(s.T().Context(), s.pg.Owner)
	s.Require().ErrorContains(err, "at least 12 characters")
}

// FirstAdminFlagsSuite reads the first administrator from migrate's flags,
// which the environment fills.
type FirstAdminFlagsSuite struct {
	suite.Suite
}

func TestFirstAdminFlags(t *testing.T) { suite.Run(t, new(FirstAdminFlagsSuite)) }

// read runs a migrate-shaped command over args and returns what
// firstAdminOf made of them.
func (s *FirstAdminFlagsSuite) read(args ...string) (firstAdmin, error) {
	var (
		first firstAdmin
		err   error
	)
	cmd := migrateCommand()
	cmd.Action = func(_ context.Context, cmd *cli.Command) error {
		first, err = firstAdminOf(cmd)
		return nil
	}
	s.Require().NoError(cmd.Run(s.T().Context(), append([]string{"migrate"}, args...)))
	return first, err
}

func (s *FirstAdminFlagsSuite) TestNeitherNamesNoAdministrator() {
	first, err := s.read()
	s.Require().NoError(err)
	s.Empty(first.Username)
}

func (s *FirstAdminFlagsSuite) TestTheUsernameIsLowerCased() {
	first, err := s.read("--admin-username", "Alice", "--admin-password", firstSecret)
	s.Require().NoError(err)
	s.Equal("alice", first.Username)
}

func (s *FirstAdminFlagsSuite) TestOneWithoutTheOtherIsRefused() {
	for _, args := range [][]string{
		{"--admin-username", "alice"},
		{"--admin-password", firstSecret},
	} {
		_, err := s.read(args...)
		s.Require().ErrorIs(err, errHalfAnAdmin, args)
	}
}
