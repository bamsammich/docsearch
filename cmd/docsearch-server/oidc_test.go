package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	accountmocks "github.com/bamsammich/docsearch/internal/account/mocks"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/resource"
	resourcemocks "github.com/bamsammich/docsearch/internal/resource/mocks"
)

const (
	issuer = "https://issuer.example"
	jwt    = "header.payload.signature"
)

// OIDCSuite serves the oidc guard over real HTTP, with the token reader and
// the account store mocked, and checks what a client sees.
type OIDCSuite struct {
	suite.Suite
	reader *resourcemocks.MockReader
	store  *accountmocks.MockStore
	site   *httptest.Server
	// seen is the user the last request reached the handler as.
	seen string
}

func TestOIDC(t *testing.T) { suite.Run(t, new(OIDCSuite)) }

func (s *OIDCSuite) SetupTest() {
	s.reader = resourcemocks.NewMockReader(s.T())
	s.store = accountmocks.NewMockStore(s.T())
	s.seen = ""

	mux := http.NewServeMux()
	s.site = httptest.NewServer(mux)
	s.T().Cleanup(s.site.Close)

	guard, err := oidcAccess(config.OIDC{
		Issuer:   issuer,
		Resource: s.resource(),
	}, readers{signed: s.reader, opaque: resource.Unintrospected{}},
		s.store, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	mux.Handle("/mcp", guard.guard(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		s.seen, _ = owner.From(r.Context()) //nolint:errcheck // an empty user fails the assertion
	})))
	for _, r := range guard.public {
		mux.Handle(r.path, r.handler)
	}
}

func (s *OIDCSuite) resource() string { return s.site.URL + "/mcp" }

func (s *OIDCSuite) post(token string) *http.Response {
	request, err := http.NewRequestWithContext(s.T().Context(), http.MethodPost, s.resource(), nil)
	s.Require().NoError(err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := s.site.Client().Do(request)
	s.Require().NoError(err)
	_, err = io.Copy(io.Discard, response.Body)
	s.Require().NoError(err)
	s.Require().NoError(response.Body.Close())
	return response
}

// A client with no token follows the challenge to the metadata, and the
// metadata to the provider. That chain is how an MCP client signs in.
func (s *OIDCSuite) TestAClientFollowsTheChallengeToTheIssuer() {
	response := s.post("")
	s.Equal(http.StatusUnauthorized, response.StatusCode)

	challenges, err := oauthex.ParseWWWAuthenticate(response.Header.Values("WWW-Authenticate"))
	s.Require().NoError(err)
	s.Require().NotEmpty(challenges)
	metadataURL := challenges[0].Params["resource_metadata"]

	metadata, err := oauthex.GetProtectedResourceMetadata(s.T().Context(),
		metadataURL, s.resource(), s.site.Client())
	s.Require().NoError(err)
	s.Equal([]string{issuer}, metadata.AuthorizationServers)
}

func (s *OIDCSuite) TestATokenActsAsItsAccount() {
	alice := resource.Identity{Issuer: issuer, Subject: "alice"}
	s.reader.EXPECT().Read(mock.Anything, jwt).Return(resource.Claims{
		Expiry: time.Now().Add(time.Hour), Issuer: issuer, Subject: "alice",
		Audience: []string{s.resource()},
	}, nil)
	s.store.EXPECT().Find(mock.Anything, alice).Return("user-a", true, nil)

	s.Equal(http.StatusOK, s.post(jwt).StatusCode)
	s.Equal("user-a", s.seen)
}

func (s *OIDCSuite) TestATokenForAnotherServiceNeverReachesAnAccount() {
	s.reader.EXPECT().Read(mock.Anything, jwt).Return(resource.Claims{
		Expiry: time.Now().Add(time.Hour), Issuer: issuer, Subject: "alice",
		Audience: []string{"https://elsewhere.example"},
	}, nil)

	s.Equal(http.StatusUnauthorized, s.post(jwt).StatusCode)
	s.Empty(s.seen)
	s.store.AssertNotCalled(s.T(), "Find", mock.Anything, mock.Anything)
}
