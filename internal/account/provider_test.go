package account_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/account"
	"github.com/bamsammich/docsearch/internal/account/mocks"
	"github.com/bamsammich/docsearch/internal/resource"
	resourcemocks "github.com/bamsammich/docsearch/internal/resource/mocks"
)

// The fixtures copy the shape of tokens captured from a real provider
// running a throwaway realm: a groups claim that is a list of names, in
// the access token for one client and only in userinfo for another. The
// names and identities are made up.
var (
	// firstToken is a person in eng, research and the admin group.
	firstToken = map[string]any{
		"preferred_username": "alice",
		"groups":             []any{"eng", "research", "docsearch-admins"},
	}
	// secondToken is the same person after the provider moved them out of
	// research and the admin group, and into ops.
	secondToken = map[string]any{
		"preferred_username": "alice",
		"groups":             []any{"eng", "ops"},
	}
)

// ProviderSuite checks what each token asks the store to change.
type ProviderSuite struct {
	suite.Suite
	store    *mocks.MockStore
	userinfo *mocks.MockUserinfo
}

func TestProvider(t *testing.T) { suite.Run(t, new(ProviderSuite)) }

func (s *ProviderSuite) SetupTest() {
	s.store = mocks.NewMockStore(s.T())
	s.userinfo = mocks.NewMockUserinfo(s.T())
	s.store.EXPECT().Find(mock.Anything, alice).Return("u-alice", true, nil).Maybe()
	s.store.EXPECT().Name(mock.Anything, "u-alice", "alice").Return(true, nil).Maybe()
}

func (s *ProviderSuite) accounts(provider account.Provider) *account.Accounts {
	return account.New(s.store, account.Policy{Provider: provider}, slog.New(slog.DiscardHandler))
}

// send passes one request bearing token, whose claims stated holds,
// through Protect and the middleware, and answers the status.
func (s *ProviderSuite) send(accounts *account.Accounts, token string, stated map[string]any) int {
	reader := resourcemocks.NewMockReader(s.T())
	reader.EXPECT().Read(mock.Anything, token).Return(claimsOf(alice, stated), nil)
	server, err := resource.New(resource.Config{Issuer: alice.Issuer, Resource: docsearch},
		reader, resource.Unintrospected{}, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	handler := server.Protect(accounts.Middleware(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	request := httptest.NewRequestWithContext(s.T().Context(), http.MethodPost, docsearch, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code
}

// Membership and the administrator role follow the claim from one token
// to the next.
func (s *ProviderSuite) TestMembershipAndAdminFollowTheClaimAcrossTwoTokens() {
	accounts := s.accounts(account.Provider{GroupsClaim: "groups", AdminGroup: "docsearch-admins"})
	s.store.EXPECT().Sync(mock.Anything, "u-alice", account.ProviderSync{
		Carried:  []string{"docsearch-admins", "eng", "research"},
		SetAdmin: true, Admin: true,
	}).Return(nil).Once()
	s.store.EXPECT().Sync(mock.Anything, "u-alice", account.ProviderSync{
		Carried:  []string{"eng", "ops"},
		SetAdmin: true, Admin: false,
	}).Return(nil).Once()

	s.Equal(http.StatusOK, s.send(accounts, "h.one.s", firstToken))
	s.Equal(http.StatusOK, s.send(accounts, "h.two.s", secondToken))
}

// A token is read once, however many requests carry it, so a change made
// in docsearch holds until the next token.
func (s *ProviderSuite) TestATokenIsSyncedOnce() {
	accounts := s.accounts(account.Provider{GroupsClaim: "groups", AdminGroup: "docsearch-admins"})
	s.store.EXPECT().Sync(mock.Anything, "u-alice", mock.Anything).Return(nil).Once()

	s.Equal(http.StatusOK, s.send(accounts, "h.one.s", firstToken))
	s.Equal(http.StatusOK, s.send(accounts, "h.one.s", firstToken))
}

// With no admin group, the provider decides memberships and leaves the
// administrator role to docsearch.
func (s *ProviderSuite) TestNoAdminGroupLeavesTheRoleAlone() {
	accounts := s.accounts(account.Provider{GroupsClaim: "groups"})
	s.store.EXPECT().Sync(mock.Anything, "u-alice", account.ProviderSync{
		Carried: []string{"docsearch-admins", "eng", "research"},
	}).Return(nil).Once()
	s.Equal(http.StatusOK, s.send(accounts, "h.one.s", firstToken))
}

// Without a groups claim the link is off, and nothing is synced.
func (s *ProviderSuite) TestNoGroupsClaimSyncsNothing() {
	s.Equal(
		http.StatusOK,
		s.send(s.accounts(account.Provider{AdminGroup: "x"}), "h.one.s", firstToken),
	)
	s.store.AssertNotCalled(s.T(), "Sync", mock.Anything, mock.Anything, mock.Anything)
}

// A provider that leaves the claim out of its access tokens is asked for
// it at userinfo, about the token's own subject.
func (s *ProviderSuite) TestAClaimMissingFromTheTokenIsReadFromUserinfo() {
	accounts := s.accounts(account.Provider{Userinfo: s.userinfo, GroupsClaim: "groups"})
	s.userinfo.EXPECT().Claims(mock.Anything, "h.one.s", alice.Subject).
		Return(map[string]any{"sub": alice.Subject, "groups": []any{"eng"}}, nil).Once()
	s.store.EXPECT().Sync(mock.Anything, "u-alice", account.ProviderSync{Carried: []string{"eng"}}).
		Return(nil).Once()

	s.Equal(
		http.StatusOK,
		s.send(accounts, "h.one.s", map[string]any{"preferred_username": "alice"}),
	)
}

// A token whose groups cannot be read is refused rather than given the
// access of no groups, or of the groups it had before.
func (s *ProviderSuite) TestUnreadableGroupsRefuseTheRequest() {
	accounts := s.accounts(account.Provider{Userinfo: s.userinfo, GroupsClaim: "groups"})
	s.userinfo.EXPECT().Claims(mock.Anything, "h.one.s", alice.Subject).
		Return(nil, errors.New("provider down")).Twice()

	s.Equal(http.StatusBadGateway, s.send(accounts, "h.one.s", map[string]any{}))
	s.Equal(http.StatusBadGateway, s.send(accounts, "h.one.s", map[string]any{}),
		"a failed token is tried again")
	s.store.AssertNotCalled(s.T(), "Sync", mock.Anything, mock.Anything, mock.Anything)
}

// A store that fails leaves the token to be tried again.
func (s *ProviderSuite) TestAFailedSyncIsRetried() {
	accounts := s.accounts(account.Provider{GroupsClaim: "groups"})
	s.store.EXPECT().
		Sync(mock.Anything, "u-alice", mock.Anything).
		Return(errors.New("db down")).
		Once()
	s.store.EXPECT().Sync(mock.Anything, "u-alice", mock.Anything).Return(nil).Once()

	s.Equal(http.StatusInternalServerError, s.send(accounts, "h.one.s", firstToken))
	s.Equal(http.StatusOK, s.send(accounts, "h.one.s", firstToken))
}

// Auto-creation asks for a group for each carried name the filter matches.
func (s *ProviderSuite) TestAutoCreateTakesWhatTheFilterMatches() {
	accounts := s.accounts(account.Provider{
		GroupsClaim: "groups", AutoCreate: true, Filter: regexp.MustCompile(`^(eng|ops)$`),
	})
	s.store.EXPECT().Sync(mock.Anything, "u-alice", account.ProviderSync{
		Carried: []string{"eng", "ops"}, Create: []string{"eng", "ops"},
	}).Return(nil).Once()
	s.Equal(http.StatusOK, s.send(accounts, "h.two.s", secondToken))
}

func (s *ProviderSuite) TestGroupNamesReadEveryShape() {
	for name, c := range map[string]struct {
		value any
		want  []string
	}{
		"a list":                {[]any{"ops", "eng", "eng", ""}, []string{"eng", "ops"}},
		"one name":              {"eng", []string{"eng"}},
		"an object keyed by it": {map[string]any{"eng": map[string]any{"id": "1"}}, []string{"eng"}},
		"a list of non-strings": {[]any{1, true}, nil},
		"nothing":               {nil, nil},
	} {
		s.Run(name, func() { s.Equal(c.want, account.GroupNames(c.value)) })
	}
}
