package pgaccount_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/account/pgaccount"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/resource"
	"github.com/bamsammich/docsearch/internal/schema"
)

var (
	alice = resource.Identity{Issuer: "https://issuer.example", Subject: "alice"}
	bob   = resource.Identity{Issuer: "https://issuer.example", Subject: "bob"}
)

// StoreSuite runs against a real database as the request role, because every
// claim it makes is about what the definer functions let that role do.
type StoreSuite struct {
	suite.Suite
	store *pgaccount.Store
}

func TestStore(t *testing.T) { suite.Run(t, new(StoreSuite)) }

func (s *StoreSuite) SetupTest() {
	pg := pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), pg.Owner))
	s.store = pgaccount.New(pg.App)
}

func (s *StoreSuite) TestAnUnknownIdentityHasNoAccount() {
	_, found, err := s.store.Find(s.T().Context(), alice)
	s.Require().NoError(err)
	s.False(found)
}

func (s *StoreSuite) TestACreatedAccountIsFound() {
	ctx := s.T().Context()
	s.Require().NoError(s.store.Create(ctx, "user-a", alice))

	user, found, err := s.store.Find(ctx, alice)
	s.Require().NoError(err)
	s.True(found)
	s.Equal("user-a", user)
}

// The unique index is what turns a race between two first requests into one
// account rather than two.
func (s *StoreSuite) TestOneIdentityIsOneAccount() {
	ctx := s.T().Context()
	s.Require().NoError(s.store.Create(ctx, "user-a", alice))
	s.Error(s.store.Create(ctx, "user-b", alice))
}

func (s *StoreSuite) TestTheBuiltinLibraryIsClaimedOnce() {
	ctx := s.T().Context()
	user, claimed, err := s.store.ClaimBuiltin(ctx, alice)
	s.Require().NoError(err)
	s.True(claimed)
	s.Equal(owner.Builtin, user)

	found, ok, err := s.store.Find(ctx, alice)
	s.Require().NoError(err)
	s.True(ok)
	s.Equal(owner.Builtin, found, "the claim is stored, not decided again each time")

	_, claimed, err = s.store.ClaimBuiltin(ctx, bob)
	s.Require().NoError(err)
	s.False(claimed, "a second identity cannot take the library from the first")
}

// Users is every account, the built-in one included, for a worker that
// serves them all.
func (s *StoreSuite) TestUsersListsEveryAccount() {
	ctx := s.T().Context()
	s.Require().NoError(s.store.Create(ctx, "user-a", alice))
	users, err := s.store.Users(ctx)
	s.Require().NoError(err)
	s.Equal([]string{owner.Builtin, "user-a"}, users)
}
