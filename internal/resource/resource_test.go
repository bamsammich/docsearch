package resource_test

import (
	"crypto"
	"crypto/rsa"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/resource"
	"github.com/bamsammich/docsearch/internal/resource/mocks"
	"github.com/bamsammich/docsearch/internal/resource/resourcetest"
	"github.com/bamsammich/docsearch/internal/resource/signed"
)

const (
	issuer    = "https://issuer.example"
	docsearch = "https://docs.example.com/mcp"
	// jwt is shaped like a JWS, which is all the routing looks at.
	jwt    = "header.payload.signature"
	opaque = "opaque-token"
)

// ServerSuite checks what the resource server decides about claims, with the
// readers mocked so each case states exactly what the issuer vouched for.
type ServerSuite struct {
	suite.Suite
	signed *mocks.MockReader
	opaque *mocks.MockReader
}

func TestServer(t *testing.T) { suite.Run(t, new(ServerSuite)) }

func (s *ServerSuite) SetupTest() {
	s.signed = mocks.NewMockReader(s.T())
	s.opaque = mocks.NewMockReader(s.T())
}

func (s *ServerSuite) server(cfg resource.Config) *resource.Server {
	if cfg.Issuer == "" {
		cfg.Issuer = issuer
	}
	if cfg.Resource == "" {
		cfg.Resource = docsearch
	}
	server, err := resource.New(cfg, s.signed, s.opaque, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	return server
}

// good is what an issuer vouches for about a token minted for docsearch.
func good() resource.Claims {
	return resource.Claims{
		Issuer:   issuer,
		Subject:  "subject-1",
		Audience: []string{docsearch},
		Scopes:   []string{"docs:read"},
		Expiry:   time.Now().Add(time.Hour),
	}
}

// answer is one request through Protect: the response, and the identity the
// handler saw if it ran at all.
type answer struct {
	identityErr error
	identity    resource.Identity
	challenge   string
	body        string
	code        int
	reached     bool
}

func (s *ServerSuite) send(server *resource.Server, token string) answer {
	var got answer
	handler := server.Protect(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got.reached = true
		got.identity, got.identityErr = resource.IdentityOf(auth.TokenInfoFromContext(r.Context()))
	}))
	request := httptest.NewRequestWithContext(s.T().Context(), http.MethodPost, docsearch, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	got.code = recorder.Code
	got.challenge = recorder.Header().Get("WWW-Authenticate")
	got.body = recorder.Body.String()
	return got
}

func (s *ServerSuite) TestATokenForDocsearchNamesItsIdentity() {
	s.signed.EXPECT().Read(mock.Anything, jwt).Return(good(), nil)
	got := s.send(s.server(resource.Config{}), jwt)
	s.Equal(http.StatusOK, got.code)
	s.True(got.reached)
	s.Require().NoError(got.identityErr)
	s.Equal(resource.Identity{Issuer: issuer, Subject: "subject-1"}, got.identity)
}

// A request with no credential is pointed at the metadata, which is how a
// client learns where to get one.
func (s *ServerSuite) TestNoTokenIsPointedAtTheMetadata() {
	got := s.send(s.server(resource.Config{}), "")
	s.Equal(http.StatusUnauthorized, got.code)
	s.Contains(got.challenge,
		`resource_metadata="https://docs.example.com/.well-known/oauth-protected-resource/mcp"`)
	s.False(got.reached)
}

func (s *ServerSuite) TestATokenNotForDocsearchIsRefused() {
	cases := map[string]func(*resource.Claims){
		"another audience": func(c *resource.Claims) { c.Audience = []string{"https://elsewhere.example"} },
		"no audience":      func(c *resource.Claims) { c.Audience = nil },
		"another issuer":   func(c *resource.Claims) { c.Issuer = "https://elsewhere.example" },
		"no subject":       func(c *resource.Claims) { c.Subject = "" },
	}
	for name, spoil := range cases {
		s.Run(name, func() {
			claims := good()
			spoil(&claims)
			s.signed.EXPECT().Read(mock.Anything, jwt).Return(claims, nil).Once()
			got := s.send(s.server(resource.Config{}), jwt)
			s.Equal(http.StatusUnauthorized, got.code)
			s.False(got.reached)
		})
	}
}

// RFC 9728 spells a resource with or without its trailing slash, and
// providers echo whichever they were given.
func (s *ServerSuite) TestATrailingSlashIsTheSameResource() {
	claims := good()
	claims.Audience = []string{docsearch + "/"}
	s.signed.EXPECT().Read(mock.Anything, jwt).Return(claims, nil)
	s.Equal(http.StatusOK, s.send(s.server(resource.Config{}), jwt).code)
}

// A provider that binds an audience of its own, such as a project
// identifier, is configured with that value, and then only that value will
// do: docsearch's own URI stops being enough.
func (s *ServerSuite) TestAConfiguredAudienceReplacesTheResource() {
	server := s.server(resource.Config{Audience: "project-1234"})

	bound := good()
	bound.Audience = []string{"client-1", "project-1234"}
	s.signed.EXPECT().Read(mock.Anything, jwt).Return(bound, nil).Once()
	s.Equal(http.StatusOK, s.send(server, jwt).code)

	s.signed.EXPECT().Read(mock.Anything, jwt).Return(good(), nil).Once()
	s.Equal(http.StatusUnauthorized, s.send(server, jwt).code)
}

func (s *ServerSuite) TestAMissingScopeIsForbidden() {
	s.signed.EXPECT().Read(mock.Anything, jwt).Return(good(), nil)
	got := s.send(s.server(resource.Config{RequiredScopes: []string{"docs:write"}}), jwt)
	s.Equal(http.StatusForbidden, got.code)
	s.Contains(got.challenge, `scope="docs:write"`)
	s.False(got.reached)
}

func (s *ServerSuite) TestAnOpaqueTokenGoesToTheIssuer() {
	s.opaque.EXPECT().Read(mock.Anything, opaque).Return(good(), nil)
	s.Equal(http.StatusOK, s.send(s.server(resource.Config{}), opaque).code)
	s.signed.AssertNotCalled(s.T(), "Read", mock.Anything, mock.Anything)
}

func (s *ServerSuite) TestAnOpaqueTokenWithoutIntrospectionSaysSo() {
	server, err := resource.New(resource.Config{Issuer: issuer, Resource: docsearch},
		s.signed, resource.Unintrospected{}, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	got := s.send(server, opaque)
	s.Equal(http.StatusUnauthorized, got.code)
	s.Contains(got.body, "no introspection is configured")
}

// A token nobody could check is not a bad token, and why it could not be
// checked stays in the log rather than in the response.
func (s *ServerSuite) TestAnUncheckedTokenKeepsItsCauseToItself() {
	s.opaque.EXPECT().Read(mock.Anything, opaque).
		Return(resource.Claims{}, errors.New("dial tcp 10.0.0.7:443: refused"))
	got := s.send(s.server(resource.Config{}), opaque)
	s.Equal(http.StatusInternalServerError, got.code)
	s.NotContains(got.body, "10.0.0.7")
}

func (s *ServerSuite) TestTheMetadataSitsWhereRFC9728PutsIt() {
	cases := map[string]string{
		"https://docs.example.com/mcp":  "https://docs.example.com/.well-known/oauth-protected-resource/mcp",
		"https://docs.example.com/mcp/": "https://docs.example.com/.well-known/oauth-protected-resource/mcp",
		"https://docs.example.com":      "https://docs.example.com/.well-known/oauth-protected-resource",
		"https://docs.example.com/":     "https://docs.example.com/.well-known/oauth-protected-resource",
	}
	for resourceURI, want := range cases {
		s.Run(resourceURI, func() {
			s.Equal(want, s.server(resource.Config{Resource: resourceURI}).MetadataURL())
		})
	}
}

func (s *ServerSuite) TestAConfigurationThatCannotWorkIsRefused() {
	cases := map[string]resource.Config{
		"no issuer":          {Resource: docsearch},
		"a relative URI":     {Issuer: issuer, Resource: "/mcp"},
		"a URI with no host": {Issuer: issuer, Resource: "https:///mcp"},
		"a fragment":         {Issuer: issuer, Resource: docsearch + "#tools"},
	}
	for name, cfg := range cases {
		s.Run(name, func() {
			_, err := resource.New(cfg, s.signed, s.opaque, slog.New(slog.DiscardHandler))
			s.Error(err)
		})
	}
}

// FlowSuite serves the resource server over real HTTP with real signed
// tokens, and fetches the metadata the way an MCP client does.
type FlowSuite struct {
	suite.Suite
	key    *rsa.PrivateKey
	site   *httptest.Server
	server *resource.Server
}

func TestFlow(t *testing.T) { suite.Run(t, new(FlowSuite)) }

func (s *FlowSuite) SetupTest() {
	s.key = resourcetest.NewKey(s.T())
	reader := signed.New(issuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&s.key.PublicKey}})

	mux := http.NewServeMux()
	s.site = httptest.NewServer(mux)
	s.T().Cleanup(s.site.Close)
	var err error
	s.server, err = resource.New(resource.Config{
		Issuer:          issuer,
		Resource:        s.site.URL + "/mcp",
		ScopesSupported: []string{"openid", "docs:read"},
	}, reader, resource.Unintrospected{}, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	mux.Handle(s.server.MetadataPath(), s.server.Metadata())
	mux.Handle(
		"/mcp",
		s.server.Protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})),
	)
}

func (s *FlowSuite) TestAClientFindsTheIssuerInTheMetadata() {
	got, err := oauthex.GetProtectedResourceMetadata(s.T().Context(),
		s.server.MetadataURL(), s.site.URL+"/mcp", s.site.Client())
	s.Require().NoError(err)
	s.Equal([]string{issuer}, got.AuthorizationServers)
	s.Equal([]string{"openid", "docs:read"}, got.ScopesSupported)
}

func (s *FlowSuite) post(token string) int {
	request, err := http.NewRequestWithContext(
		s.T().Context(),
		http.MethodPost,
		s.site.URL+"/mcp",
		nil,
	)
	s.Require().NoError(err)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := s.site.Client().Do(request)
	s.Require().NoError(err)
	_, err = io.Copy(io.Discard, response.Body)
	s.Require().NoError(err)
	s.Require().NoError(response.Body.Close())
	return response.StatusCode
}

// The case the specification names twice: a genuine token from the right
// issuer, minted for a different service, is refused.
func (s *FlowSuite) TestASignedTokenIsHeldToItsAudience() {
	claims := map[string]any{
		"iss": issuer, "sub": "subject-1",
		"aud": s.site.URL + "/mcp", "exp": time.Now().Add(time.Hour).Unix(),
	}
	s.Equal(http.StatusOK, s.post(resourcetest.Sign(s.T(), s.key, claims)))

	claims["aud"] = "https://elsewhere.example"
	s.Equal(http.StatusUnauthorized, s.post(resourcetest.Sign(s.T(), s.key, claims)))
}
