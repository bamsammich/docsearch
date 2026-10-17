package pgpassword_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/password/pgpassword"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

// StoreSuite administers accounts as the owner and signs in as the request
// role, the two credentials password mode uses in production.
type StoreSuite struct {
	suite.Suite
	pg    *pgtest.DB
	admin *pgpassword.Store
	app   *pgpassword.Store
}

func TestStore(t *testing.T) { suite.Run(t, new(StoreSuite)) }

func (s *StoreSuite) SetupTest() {
	s.pg = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.pg.Owner))
	s.admin = pgpassword.New(s.pg.Owner)
	s.app = pgpassword.New(s.pg.App)
}

// An account comes with a library of its own, whose identity is the pair a
// docsearch-issued token resolves to.
func (s *StoreSuite) TestAnAccountComesWithItsOwnLibrary() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))

	var issuer, subject string
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT issuer, subject FROM users WHERE user_id = 'user-a'`).Scan(&issuer, &subject))
	s.Equal("docsearch", issuer)
	s.Equal("user-a", subject)

	var partitions int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_class WHERE relname = 'chunks_user-a'`).Scan(&partitions))
	s.Equal(1, partitions, "the library is partitioned like any other")

	record, found, err := s.app.Find(ctx, "alice")
	s.Require().NoError(err)
	s.True(found)
	s.Equal("user-a", record.UserID)
	s.Equal("hash", record.Hash)
}

func (s *StoreSuite) TestTheBuiltinLibraryIsTakenOnce() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", owner.Builtin, "hash", true))
	s.Require().ErrorIs(s.admin.Add(ctx, "bob", owner.Builtin, "hash", true),
		password.ErrBuiltinTaken)

	_, found, err := s.app.Find(ctx, "bob")
	s.Require().NoError(err)
	s.False(found, "the refused account left nothing behind")
}

func (s *StoreSuite) TestFailuresLockAndANewPasswordUnlocks() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	for range 2 {
		s.Require().NoError(s.app.Failed(ctx, "alice", 2, until))
	}
	record, _, err := s.app.Find(ctx, "alice")
	s.Require().NoError(err)
	s.Equal(2, record.Failures)
	s.True(record.LockedUntil.Equal(until), "the second failure reached the threshold")

	s.Require().NoError(s.admin.SetHash(ctx, "alice", "new-hash"))
	record, _, err = s.app.Find(ctx, "alice")
	s.Require().NoError(err)
	s.Zero(record.Failures)
	s.True(record.LockedUntil.IsZero())
	s.Equal("new-hash", record.Hash)
}

// Removing an account ends its sign-in, and the library stays.
func (s *StoreSuite) TestRemovingAnAccountEndsItsSignIn() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	present, err := s.app.Exists(ctx, "user-a")
	s.Require().NoError(err)
	s.True(present)

	s.Require().NoError(s.admin.Remove(ctx, "alice"))
	present, err = s.app.Exists(ctx, "user-a")
	s.Require().NoError(err)
	s.False(present)

	var libraries int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE user_id = 'user-a'`).Scan(&libraries))
	s.Equal(1, libraries)

	s.Require().ErrorIs(s.admin.Remove(ctx, "alice"), password.ErrNoSuchAccount)
	s.Require().ErrorIs(s.admin.SetHash(ctx, "alice", "x"), password.ErrNoSuchAccount)
}

func (s *StoreSuite) TestListShowsEveryAccount() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "bob", "user-b", "hash", false))
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	list, err := s.admin.List(ctx)
	s.Require().NoError(err)
	s.Require().Len(list, 2)
	s.Equal("alice", list[0].Username)
}
