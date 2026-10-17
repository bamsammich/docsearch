package password_test

import (
	"context"
	"testing"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/password/mocks"
)

const secret = "correct horse battery"

var now = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

type PasswordSuite struct {
	suite.Suite
	store    *mocks.MockStore
	accounts *password.Accounts
	hash     string
}

func TestPassword(t *testing.T) { suite.Run(t, new(PasswordSuite)) }

func (s *PasswordSuite) SetupTest() {
	s.store = mocks.NewMockStore(s.T())
	accounts, err := password.New(s.store, func() time.Time { return now })
	s.Require().NoError(err)
	s.accounts = accounts
	s.hash, err = argon2id.CreateHash(secret, argon2id.DefaultParams)
	s.Require().NoError(err)
}

func (s *PasswordSuite) record(lockedUntil time.Time) password.Record {
	return password.Record{Hash: s.hash, Account: password.Account{
		Username: "alice", UserID: "user-a", LockedUntil: lockedUntil,
	}}
}

func (s *PasswordSuite) TestTheRightPasswordSignsInAndClearsFailures() {
	s.store.EXPECT().Find(mock.Anything, "alice").Return(s.record(time.Time{}), true, nil)
	s.store.EXPECT().Succeeded(mock.Anything, "alice").Return(nil)
	user, err := s.accounts.Authenticate(s.T().Context(), "Alice", secret)
	s.Require().NoError(err)
	s.Equal("user-a", user)
}

func (s *PasswordSuite) TestAWrongPasswordCountsTowardsALock() {
	s.store.EXPECT().Find(mock.Anything, "alice").Return(s.record(time.Time{}), true, nil)
	s.store.EXPECT().Failed(mock.Anything, "alice", password.LockThreshold,
		now.Add(password.LockDuration)).Return(nil)
	_, err := s.accounts.Authenticate(s.T().Context(), "alice", "not the password")
	s.Require().ErrorIs(err, password.ErrSignInRefused)
}

// A locked account is refused before its password is even checked, so a
// guess made during the lock tells an attacker nothing.
func (s *PasswordSuite) TestALockedAccountRefusesEvenTheRightPassword() {
	s.store.EXPECT().Find(mock.Anything, "alice").
		Return(s.record(now.Add(time.Minute)), true, nil)
	_, err := s.accounts.Authenticate(s.T().Context(), "alice", secret)
	s.Require().ErrorIs(err, password.ErrSignInRefused)
	s.store.AssertNotCalled(s.T(), "Succeeded", mock.Anything, mock.Anything)
}

// An unknown username is refused the same way as a wrong password, so the
// page cannot be used to find which names exist.
func (s *PasswordSuite) TestAnUnknownUsernameReadsLikeAWrongPassword() {
	s.store.EXPECT().Find(mock.Anything, "mallory").Return(password.Record{}, false, nil)
	_, err := s.accounts.Authenticate(s.T().Context(), "mallory", secret)
	s.Require().ErrorIs(err, password.ErrSignInRefused)
}

func (s *PasswordSuite) TestAnAccountGetsANewLibraryOrTheBuiltinOne() {
	var hashed string
	s.store.EXPECT().Add(mock.Anything, "alice", mock.Anything, mock.Anything, false).
		RunAndReturn(func(_ context.Context, _, userID, hash string, _ bool) error {
			s.Regexp(`^[a-z0-9]{26}$`, userID)
			hashed = hash
			return nil
		})
	s.Require().NoError(s.accounts.Add(s.T().Context(), "alice", secret, false))
	match, err := argon2id.ComparePasswordAndHash(secret, hashed)
	s.Require().NoError(err)
	s.True(match, "the store gets a hash, never the password")

	s.store.EXPECT().Add(mock.Anything, "bob", password.Builtin, mock.Anything, true).Return(nil)
	s.Require().NoError(s.accounts.Add(s.T().Context(), "bob", secret, true))
}

func (s *PasswordSuite) TestAWeakPasswordOrABadUsernameIsRefused() {
	s.Require().Error(s.accounts.Add(s.T().Context(), "alice", "short", false))
	s.Require().Error(s.accounts.Add(s.T().Context(), "Alice Smith", secret, false))
	s.Require().Error(s.accounts.SetPassword(s.T().Context(), "alice", "short"))
	s.store.AssertNotCalled(s.T(), "Add", mock.Anything, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything)
}
