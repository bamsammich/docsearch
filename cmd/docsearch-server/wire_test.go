package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
)

// AuthSuite covers what an auth mode changes about a request, which is the
// credential it must carry and nothing else: both built modes answer with
// the built-in user.
type AuthSuite struct{ suite.Suite }

func TestAuth(t *testing.T) { suite.Run(t, new(AuthSuite)) }

// served reports the status a request gets, and the user the handler saw.
//
// The owner is read inside the handler and asserted outside it, because a
// failed assertion stops the goroutine it runs on rather than the test's.
func served(mode owner.Mode, header string) (int, string) {
	var seen string
	handler := authenticated(
		config.Config{Auth: mode, BearerToken: "right"},
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			// A handler reached without an owner reads as the empty string,
			// which is what the refusal cases assert.
			seen, _ = owner.From(r.Context()) //nolint:errcheck // the empty answer is the assertion
		}))

	request := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	if header != "" {
		request.Header.Set("Authorization", header)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code, seen
}

func (s *AuthSuite) TestTokenModeRefusesARequestWithNoCredential() {
	status, seen := served(owner.Token, "")
	s.Equal(http.StatusUnauthorized, status)
	s.Empty(seen, "a refused request never reaches a handler holding a user")
}

func (s *AuthSuite) TestTokenModeRefusesTheWrongCredential() {
	status, _ := served(owner.Token, "Bearer wrong")
	s.Equal(http.StatusUnauthorized, status)
}

func (s *AuthSuite) TestTokenModeNamesTheBuiltInUser() {
	status, seen := served(owner.Token, "Bearer right")
	s.Equal(http.StatusOK, status)
	s.Equal(owner.Builtin, seen)
}

// Mode none asks for no credential, and still names a user: a handler that
// reached owner.From with nothing would be refused by the layer phase 04
// built, so the mode has to answer rather than skip.
func (s *AuthSuite) TestNoneModeServesWithNoCredentialAndStillNamesTheUser() {
	status, seen := served(owner.None, "")
	s.Equal(http.StatusOK, status)
	s.Equal(owner.Builtin, seen)
}

// The mode changes the credential, not the library. Config.Validate refuses
// none off loopback, so the two differ in what they ask for and agree on
// whose documents they serve.
func (s *AuthSuite) TestBothModesAnswerWithTheSameUser() {
	_, withToken := served(owner.Token, "Bearer right")
	_, without := served(owner.None, "")
	s.Equal(withToken, without)
}
