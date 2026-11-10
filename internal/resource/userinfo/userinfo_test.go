package userinfo_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/resource/userinfo"
)

// UserinfoSuite answers as a provider's userinfo endpoint would.
type UserinfoSuite struct {
	suite.Suite
	answer map[string]any
	reader *userinfo.Reader
	bearer string
	status int
}

func TestUserinfo(t *testing.T) { suite.Run(t, new(UserinfoSuite)) }

func (s *UserinfoSuite) SetupTest() {
	s.status = http.StatusOK
	s.answer = map[string]any{"sub": "alice", "groups": []any{"eng"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.bearer = r.Header.Get("Authorization")
		w.WriteHeader(s.status)
		s.NoError(json.NewEncoder(w).Encode(s.answer))
	}))
	s.T().Cleanup(server.Close)
	s.reader = userinfo.New(server.URL, server.Client())
}

func (s *UserinfoSuite) TestClaimsAreReadWithTheToken() {
	claims, err := s.reader.Claims(s.T().Context(), "the-token", "alice")
	s.Require().NoError(err)
	s.Equal([]any{"eng"}, claims["groups"])
	s.Equal("Bearer the-token", s.bearer)
}

// OpenID Connect Core 5.3.2: a response about another subject is not used.
func (s *UserinfoSuite) TestAnotherSubjectIsRefused() {
	s.answer["sub"] = "mallory"
	_, err := s.reader.Claims(s.T().Context(), "the-token", "alice")
	s.Require().ErrorIs(err, userinfo.ErrSubject)
}

func (s *UserinfoSuite) TestARefusalIsAnError() {
	s.status = http.StatusUnauthorized
	_, err := s.reader.Claims(s.T().Context(), "the-token", "alice")
	s.Require().ErrorContains(err, "401")
}
