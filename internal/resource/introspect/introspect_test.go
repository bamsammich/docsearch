package introspect_test

import (
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/bamsammich/docsearch/internal/resource"
	"github.com/bamsammich/docsearch/internal/resource/introspect"
	"github.com/bamsammich/docsearch/internal/resource/introspect/mocks"
)

const (
	issuer    = "https://issuer.example"
	docsearch = "https://docs.example.com/mcp"
)

// IntrospectSuite states what the issuer answered and checks what the reader
// makes of it. The request itself is the library's.
type IntrospectSuite struct {
	suite.Suite
	introspector *mocks.MockIntrospector
	reader       *introspect.Reader
}

func TestIntrospect(t *testing.T) { suite.Run(t, new(IntrospectSuite)) }

func (s *IntrospectSuite) SetupTest() {
	s.introspector = mocks.NewMockIntrospector(s.T())
	s.reader = introspect.New(issuer, s.introspector)
}

func (s *IntrospectSuite) answers(response oidc.IntrospectionResponse, err error) {
	s.introspector.EXPECT().Introspect(mock.Anything, "opaque-1").Return(response, err)
}

func (s *IntrospectSuite) TestReadsWhatTheIssuerSays() {
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	s.answers(oidc.IntrospectionResponse{
		Active:     true,
		Issuer:     issuer,
		Subject:    "subject-1",
		Audience:   oidc.Audience{docsearch},
		Scope:      oidc.SpaceDelimitedArray{"openid", "docs:read"},
		Expiration: oidc.FromTime(expiry),
		Claims:     map[string]any{"groups": []any{"docsearch-users"}},
	}, nil)

	got, err := s.reader.Read(s.T().Context(), "opaque-1")
	s.Require().NoError(err)
	s.Equal(resource.Claims{
		Issuer:   issuer,
		Subject:  "subject-1",
		Audience: []string{docsearch},
		Scopes:   []string{"openid", "docs:read"},
		Expiry:   expiry,
		Stated:   map[string]any{"groups": []any{"docsearch-users"}},
	}, got)
}

// iss is optional in an introspection response, and the endpoint that
// answered belongs to the configured issuer.
func (s *IntrospectSuite) TestAnAnswerWithoutAnIssuerSpeaksForTheOneAsked() {
	s.answers(oidc.IntrospectionResponse{Active: true, Subject: "subject-1"}, nil)
	got, err := s.reader.Read(s.T().Context(), "opaque-1")
	s.Require().NoError(err)
	s.Equal(issuer, got.Issuer)
}

func (s *IntrospectSuite) TestAnInactiveTokenIsInvalid() {
	s.answers(oidc.IntrospectionResponse{Active: false, Subject: "subject-1"}, nil)
	_, err := s.reader.Read(s.T().Context(), "opaque-1")
	s.ErrorIs(err, auth.ErrInvalidToken)
}

// The issuer said nothing about the token, so a failed request must not read
// as a bad credential: the client would discard a token that is fine.
func (s *IntrospectSuite) TestAFailedRequestIsNotAnInvalidToken() {
	s.answers(oidc.IntrospectionResponse{}, errors.New("401 Unauthorized"))
	_, err := s.reader.Read(s.T().Context(), "opaque-1")
	s.Require().Error(err)
	s.NotErrorIs(err, auth.ErrInvalidToken)
}

// An inactive token stays inactive, so asking again would only cost the
// issuer a request. A failed request is not remembered: the issuer said
// nothing, and the next try may get an answer.
func (s *IntrospectSuite) TestAnInactiveTokenIsAskedAboutOnce() {
	s.introspector.EXPECT().Introspect(mock.Anything, "opaque-1").
		Return(oidc.IntrospectionResponse{Active: false}, nil).Once()
	for range 3 {
		_, err := s.reader.Read(s.T().Context(), "opaque-1")
		s.Require().ErrorIs(err, auth.ErrInvalidToken)
	}

	s.introspector.EXPECT().Introspect(mock.Anything, "opaque-2").
		Return(oidc.IntrospectionResponse{}, errors.New("503")).Twice()
	for range 2 {
		_, err := s.reader.Read(s.T().Context(), "opaque-2")
		s.Require().Error(err)
	}
}
