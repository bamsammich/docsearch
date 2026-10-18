package password_test

import (
	"context"
	"sync"
	"sync/atomic"
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
	s.hash, err = argon2id.CreateHash(secret, password.Params())
	s.Require().NoError(err)
}

func (s *PasswordSuite) record(lockedUntil time.Time) password.Record {
	return password.Record{Hash: s.hash, Account: password.Account{
		Username: "alice", UserID: "user-a", LockedUntil: lockedUntil,
	}}
}

// attempt is the attempt the policy counts at now.
var attempt = password.Attempt{
	At: now, LockUntil: now.Add(password.LockDuration), Threshold: password.LockThreshold,
}

func (s *PasswordSuite) TestTheRightPasswordSignsInAndClearsFailures() {
	s.store.EXPECT().Find(mock.Anything, "alice").Return(s.record(time.Time{}), true, nil)
	s.store.EXPECT().Begin(mock.Anything, "alice", attempt).Return(true, nil)
	s.store.EXPECT().Succeeded(mock.Anything, "alice").Return(nil)
	user, err := s.accounts.Authenticate(s.T().Context(), "Alice", secret)
	s.Require().NoError(err)
	s.Equal("user-a", user)
}

// The attempt is counted before the password is compared, so a wrong
// password has nothing left to record.
func (s *PasswordSuite) TestAWrongPasswordIsCountedBeforeItIsChecked() {
	s.store.EXPECT().Find(mock.Anything, "alice").Return(s.record(time.Time{}), true, nil)
	s.store.EXPECT().Begin(mock.Anything, "alice", attempt).Return(true, nil)
	_, err := s.accounts.Authenticate(s.T().Context(), "alice", "not the password")
	s.Require().ErrorIs(err, password.ErrSignInRefused)
	s.store.AssertNotCalled(s.T(), "Succeeded", mock.Anything, mock.Anything)
}

// A locked account is refused whatever the password, and the refusal still
// pays for a comparison, so its timing does not say the account is locked.
func (s *PasswordSuite) TestALockedAccountRefusesEvenTheRightPassword() {
	s.store.EXPECT().Find(mock.Anything, "alice").Return(s.record(time.Time{}), true, nil)
	s.store.EXPECT().Begin(mock.Anything, "alice", attempt).Return(false, nil)
	_, err := s.accounts.Authenticate(s.T().Context(), "alice", secret)
	s.Require().ErrorIs(err, password.ErrSignInRefused)
	s.store.AssertNotCalled(s.T(), "Succeeded", mock.Anything, mock.Anything)
}

// A hash made under earlier, costlier settings is replaced when its password
// next signs in, so the cheaper costs reach existing accounts.
func (s *PasswordSuite) TestAnOlderHashIsReplacedOnSignIn() {
	older, err := argon2id.CreateHash(secret, argon2id.DefaultParams)
	s.Require().NoError(err)
	record := s.record(time.Time{})
	record.Hash = older
	s.store.EXPECT().Find(mock.Anything, "alice").Return(record, true, nil)
	s.store.EXPECT().Begin(mock.Anything, "alice", attempt).Return(true, nil)
	s.store.EXPECT().SetHash(mock.Anything, "alice", mock.Anything).
		RunAndReturn(func(_ context.Context, _, hash string) error {
			_, made, err := argon2id.CheckHash(secret, hash)
			s.Require().NoError(err)
			s.Equal(*password.Params(), *made)
			return nil
		})
	user, err := s.accounts.Authenticate(s.T().Context(), "alice", secret)
	s.Require().NoError(err)
	s.Equal("user-a", user)
}

// Sign-ins past the bound wait rather than each holding a hash's memory, and
// a caller that gives up while waiting is let go.
func (s *PasswordSuite) TestSignInsBeyondTheBoundWait() {
	release := make(chan struct{})
	var arrived atomic.Int32
	s.store.EXPECT().Find(mock.Anything, "alice").
		RunAndReturn(func(context.Context, string) (password.Record, bool, error) {
			arrived.Add(1)
			<-release
			return password.Record{}, false, nil
		})
	var held sync.WaitGroup
	for range password.ConcurrentChecks {
		held.Go(func() {
			_, err := s.accounts.Authenticate(context.Background(), "alice", secret)
			s.ErrorIs(err, password.ErrSignInRefused)
		})
	}
	s.Eventually(func() bool { return arrived.Load() == password.ConcurrentChecks },
		time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(s.T().Context(), 20*time.Millisecond)
	defer cancel()
	_, err := s.accounts.Authenticate(ctx, "alice", secret)
	s.Require().ErrorIs(err, context.DeadlineExceeded)
	close(release)
	held.Wait()
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
