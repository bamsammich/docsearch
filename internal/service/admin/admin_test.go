package admin_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/service/admin"
	"github.com/bamsammich/docsearch/internal/service/admin/mocks"
)

// now is the clock every case reads.
var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// AdminSuite drives the service against mocked ports, as ana, who
// administers.
type AdminSuite struct {
	suite.Suite
	store     *mocks.MockStore
	passwords *mocks.MockPasswords
	service   *admin.Service
}

func TestAdmin(t *testing.T) { suite.Run(t, new(AdminSuite)) }

func (s *AdminSuite) SetupTest() {
	s.store = mocks.NewMockStore(s.T())
	s.passwords = mocks.NewMockPasswords(s.T())
	s.service = admin.New(s.store, s.passwords, func() string { return "generated-secret" },
		func() time.Time { return now })
}

func (s *AdminSuite) asAdmin() {
	s.store.EXPECT().Me(mock.Anything).Return(admin.Me{Username: "ana", Admin: true}, nil)
}

func (s *AdminSuite) TestSomeoneWhoDoesNotAdministerIsRefusedEverything() {
	s.store.EXPECT().Me(mock.Anything).Return(admin.Me{Username: "ben"}, nil)

	_, err := s.service.List(s.T().Context())
	s.Require().ErrorIs(err, admin.ErrNotAdmin)
	_, err = s.service.Add(s.T().Context(), "cy")
	s.Require().ErrorIs(err, admin.ErrNotAdmin)
	s.Require().ErrorIs(s.service.SetAdmin(s.T().Context(), "ben", true), admin.ErrNotAdmin)
	s.Require().ErrorIs(s.service.Remove(s.T().Context(), "cy"), admin.ErrNotAdmin)
}

// A removed password account keeps its users row, so it is left out: nobody
// can sign in as it.
func (s *AdminSuite) TestThePasswordListMarksLocksAndLeavesOutRemovedAccounts() {
	s.asAdmin()
	s.store.EXPECT().List(mock.Anything).Return([]admin.Account{
		{Username: "ana", Admin: true}, {Username: "ben"}, {Username: "gone"},
	}, nil)
	s.passwords.EXPECT().List(mock.Anything).Return([]password.Account{
		{Username: "ana", LockedUntil: now.Add(-time.Hour)},
		{Username: "ben", LockedUntil: now.Add(10 * time.Minute)},
	}, nil)

	accounts, err := s.service.List(s.T().Context())
	s.Require().NoError(err)
	s.Require().Len(accounts, 2)

	s.True(accounts[0].Password)
	s.True(accounts[0].LockedUntil.IsZero(), "a lock that has run out is no lock")
	s.Equal(now.Add(10*time.Minute), accounts[1].LockedUntil)
}

func (s *AdminSuite) TestAddingAnAccountReturnsItsPasswordAndRecordsIt() {
	s.asAdmin()
	s.passwords.EXPECT().Add(mock.Anything, "cy", "generated-secret").Return(nil)
	s.store.EXPECT().Record(mock.Anything, admin.ActionAdd, "cy").Return(nil)

	secret, err := s.service.Add(s.T().Context(), "cy")
	s.Require().NoError(err)
	s.Equal("generated-secret", secret)
}

func (s *AdminSuite) TestAResetReturnsTheNewPassword() {
	s.asAdmin()
	s.passwords.EXPECT().SetPassword(mock.Anything, "ben", "generated-secret").Return(nil)
	s.store.EXPECT().Record(mock.Anything, admin.ActionReset, "ben").Return(nil)

	secret, err := s.service.Reset(s.T().Context(), "ben")
	s.Require().NoError(err)
	s.Equal("generated-secret", secret)
}

func (s *AdminSuite) TestAnAdministratorCannotRemoveTheirOwnAccount() {
	s.asAdmin()

	s.Require().ErrorIs(s.service.Remove(s.T().Context(), "ana"), admin.ErrSelf)
}

func (s *AdminSuite) TestRemovingAnAccountEndsItsSignInAndRecordsIt() {
	s.asAdmin()
	s.passwords.EXPECT().Remove(mock.Anything, "ben").Return(nil)
	s.store.EXPECT().Record(mock.Anything, admin.ActionRemove, "ben").Return(nil)

	s.Require().NoError(s.service.Remove(s.T().Context(), "ben"))
}

// With a provider, docsearch holds no passwords: people are added and
// removed there, and an administrator only chooses who administers.
func (s *AdminSuite) TestWithAProviderPasswordActionsAreRefused() {
	service := admin.New(
		s.store,
		nil,
		func() string { return "unused" },
		func() time.Time { return now },
	)
	s.asAdmin()
	s.store.EXPECT().
		List(mock.Anything).
		Return([]admin.Account{{Username: "ana", Admin: true}}, nil)

	accounts, err := service.List(s.T().Context())
	s.Require().NoError(err)
	s.Len(accounts, 1)
	s.False(accounts[0].Password)

	_, err = service.Add(s.T().Context(), "cy")
	s.Require().ErrorIs(err, admin.ErrNoPasswords)
	s.Require().ErrorIs(service.Unlock(s.T().Context(), "ben"), admin.ErrNoPasswords)
	s.Require().ErrorIs(service.Remove(s.T().Context(), "ben"), admin.ErrNoPasswords)
}
