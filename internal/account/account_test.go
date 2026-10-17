package account_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/account"
	"github.com/bamsammich/docsearch/internal/account/mocks"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/resource"
	resourcemocks "github.com/bamsammich/docsearch/internal/resource/mocks"
)

const (
	docsearch = "https://docs.example.com/mcp"
	jwt       = "header.payload.signature"
)

var (
	alice = resource.Identity{Issuer: "https://issuer.example", Subject: "alice"}
	bob   = resource.Identity{Issuer: "https://issuer.example", Subject: "bob"}
)

// AccountSuite states what the store holds and checks which library a
// request is given.
type AccountSuite struct {
	suite.Suite
	store *mocks.MockStore
}

func TestAccount(t *testing.T) { suite.Run(t, new(AccountSuite)) }

func (s *AccountSuite) SetupTest() {
	s.store = mocks.NewMockStore(s.T())
}

func (s *AccountSuite) accounts(policy account.Policy) *account.Accounts {
	return account.New(s.store, policy, slog.New(slog.DiscardHandler))
}

func (s *AccountSuite) TestAnExistingAccountIsFound() {
	s.store.EXPECT().Find(mock.Anything, alice).Return("user-a", true, nil)
	user, err := s.accounts(account.Policy{}).Resolve(s.T().Context(), alice)
	s.Require().NoError(err)
	s.Equal("user-a", user)
}

func (s *AccountSuite) TestSignUpStartsClosed() {
	s.store.EXPECT().Find(mock.Anything, alice).Return("", false, nil)
	_, err := s.accounts(account.Policy{}).Resolve(s.T().Context(), alice)
	s.Require().ErrorIs(err, account.ErrNoAccount)
	s.store.AssertNotCalled(s.T(), "Create", mock.Anything, mock.Anything, mock.Anything)
}

func (s *AccountSuite) TestOpenSignUpCreatesAnAccount() {
	s.store.EXPECT().Find(mock.Anything, alice).Return("", false, nil)
	var created string
	s.store.EXPECT().Create(mock.Anything, mock.Anything, alice).
		RunAndReturn(func(_ context.Context, user string, _ resource.Identity) error {
			created = user
			return nil
		})

	user, err := s.accounts(account.Policy{SignUp: true}).Resolve(s.T().Context(), alice)
	s.Require().NoError(err)
	s.Equal(created, user)
	// The identifier becomes part of a table name.
	s.Regexp(`^[a-z0-9_-]{1,40}$`, user)
}

// Two first requests race to create one account; the loser reads the
// account the winner made.
func (s *AccountSuite) TestALostRaceFindsTheWinnersAccount() {
	s.store.EXPECT().Find(mock.Anything, alice).Return("", false, nil).Once()
	s.store.EXPECT().Create(mock.Anything, mock.Anything, alice).Return(errors.New("duplicate key"))
	s.store.EXPECT().Find(mock.Anything, alice).Return("user-a", true, nil).Once()

	user, err := s.accounts(account.Policy{SignUp: true}).Resolve(s.T().Context(), alice)
	s.Require().NoError(err)
	s.Equal("user-a", user)
}

// The default owner takes the built-in library even with sign-up closed: naming them
// is the operator saying they may sign in.
func (s *AccountSuite) TestTheDefaultOwnerClaimsTheBuiltinLibrary() {
	s.store.EXPECT().Find(mock.Anything, alice).Return("", false, nil)
	s.store.EXPECT().ClaimBuiltin(mock.Anything, alice).Return(owner.Builtin, true, nil)

	user, err := s.accounts(account.Policy{DefaultOwner: alice}).Resolve(s.T().Context(), alice)
	s.Require().NoError(err)
	s.Equal(owner.Builtin, user)
}

func (s *AccountSuite) TestNobodyElseClaimsTheBuiltinLibrary() {
	s.store.EXPECT().Find(mock.Anything, bob).Return("", false, nil)
	_, err := s.accounts(account.Policy{DefaultOwner: alice}).Resolve(s.T().Context(), bob)
	s.Require().ErrorIs(err, account.ErrNoAccount)
	s.store.AssertNotCalled(s.T(), "ClaimBuiltin", mock.Anything, mock.Anything)
}

// answer is one request through the middleware.
type answer struct {
	user    string
	code    int
	reached bool
}

// send passes one request through Protect and then the middleware, the order
// the server mounts them in. The token is whatever a mocked reader vouches
// for, so each case states its claims and nothing else.
func (s *AccountSuite) send(accounts *account.Accounts, claims resource.Claims) answer {
	reader := resourcemocks.NewMockReader(s.T())
	reader.EXPECT().Read(mock.Anything, jwt).Return(claims, nil)
	server, err := resource.New(resource.Config{Issuer: alice.Issuer, Resource: docsearch},
		reader, resource.Unintrospected{}, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	var got answer
	handler := server.Protect(accounts.Middleware(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got.reached = true
			//nolint:errcheck // an empty user fails the assertion
			got.user, _ = owner.From(r.Context())
		})))
	request := httptest.NewRequestWithContext(s.T().Context(), http.MethodPost, docsearch, nil)
	request.Header.Set("Authorization", "Bearer "+jwt)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	got.code = recorder.Code
	return got
}

// claimsOf is a token for docsearch naming identity, with stated claims.
func claimsOf(identity resource.Identity, stated map[string]any) resource.Claims {
	return resource.Claims{
		Expiry:   time.Now().Add(time.Hour),
		Stated:   stated,
		Issuer:   identity.Issuer,
		Subject:  identity.Subject,
		Audience: []string{docsearch},
	}
}

func (s *AccountSuite) TestARequestActsAsItsAccount() {
	s.store.EXPECT().Find(mock.Anything, alice).Return("user-a", true, nil)
	got := s.send(s.accounts(account.Policy{}), claimsOf(alice, nil))
	s.Equal(http.StatusOK, got.code)
	s.Equal("user-a", got.user)
}

func (s *AccountSuite) TestAClosedSignUpIsForbidden() {
	s.store.EXPECT().Find(mock.Anything, alice).Return("", false, nil)
	got := s.send(s.accounts(account.Policy{}), claimsOf(alice, nil))
	s.Equal(http.StatusForbidden, got.code)
	s.False(got.reached)
}

func (s *AccountSuite) TestTheRequiredClaimIsCheckedEveryTime() {
	policy := account.Policy{
		Required: account.RequiredClaim{Name: "groups", Value: "docsearch-users"},
	}
	cases := map[string]struct {
		stated map[string]any
		want   int
	}{
		"a list holding the value": {
			map[string]any{"groups": []any{"staff", "docsearch-users"}},
			http.StatusOK,
		},
		"the value itself": {map[string]any{"groups": "docsearch-users"}, http.StatusOK},
		"a list without it": {
			map[string]any{"groups": []any{"staff"}},
			http.StatusForbidden,
		},
		"another value": {map[string]any{"groups": "staff"}, http.StatusForbidden},
		"no such claim": {map[string]any{}, http.StatusForbidden},
	}
	s.store.EXPECT().Find(mock.Anything, alice).Return("user-a", true, nil).Maybe()
	for name, c := range cases {
		s.Run(name, func() {
			got := s.send(s.accounts(policy), claimsOf(alice, c.stated))
			s.Equal(c.want, got.code)
		})
	}
}
