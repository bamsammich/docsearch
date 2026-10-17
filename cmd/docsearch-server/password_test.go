package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/password/pgpassword"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

const (
	alicePassword = "correct horse battery staple"
	callback      = "http://127.0.0.1:9/callback"
)

// PasswordSuite runs an MCP client's whole sign-in against password mode:
// discovery, registration, PKCE, the sign-in page, the code exchange and
// refresh, over real HTTP and a real database.
type PasswordSuite struct {
	suite.Suite
	pg      *pgtest.DB
	site    *httptest.Server
	browser *http.Client
	admin   *pgpassword.Store
	aliceID string
}

func TestPassword(t *testing.T) { suite.Run(t, new(PasswordSuite)) }

func (s *PasswordSuite) SetupTest() {
	ctx := s.T().Context()
	s.pg = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(ctx, s.pg.Owner))
	s.admin = pgpassword.New(s.pg.Owner)
	accounts, err := password.New(s.admin, time.Now)
	s.Require().NoError(err)
	s.Require().NoError(accounts.Add(ctx, "alice", alicePassword, false))
	record, _, err := s.admin.Find(ctx, "alice")
	s.Require().NoError(err)
	s.aliceID = record.UserID

	mux := http.NewServeMux()
	s.site = httptest.NewServer(mux)
	s.T().Cleanup(s.site.Close)
	guard, err := passwordAccess(config.Config{PublicURL: s.site.URL}, s.pg.App,
		slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	for _, r := range guard.public {
		mux.Handle(r.path, r.handler)
	}
	mux.Handle("/mcp", guard.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := owner.From(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, user) //nolint:errcheck // a short write fails the assertion
	})))
	// A browser that stops at each redirect, so every hop can be checked.
	s.browser = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func (s *PasswordSuite) do(
	method, target string,
	body io.Reader,
	header map[string]string,
) *http.Response {
	request, err := http.NewRequestWithContext(s.T().Context(), method, target, body)
	s.Require().NoError(err)
	for k, v := range header {
		request.Header.Set(k, v)
	}
	response, err := s.browser.Do(request)
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = response.Body.Close() })
	return response
}

func (s *PasswordSuite) decode(response *http.Response, into any) {
	s.Require().NoError(json.NewDecoder(response.Body).Decode(into))
}

func (s *PasswordSuite) readAll(response *http.Response) string {
	body, err := io.ReadAll(response.Body)
	s.Require().NoError(err)
	return string(body)
}

// endpoints are what a client learns from the metadata.
type endpoints struct {
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	Registration  string `json:"registration_endpoint"`
}

// discover follows the 401 to the resource metadata and on to the
// authorization server's, the way an MCP client finds where to sign in.
func (s *PasswordSuite) discover() endpoints {
	refused := s.do(http.MethodPost, s.site.URL+"/mcp", nil, nil)
	s.Require().Equal(http.StatusUnauthorized, refused.StatusCode)
	s.Require().Contains(refused.Header.Get("WWW-Authenticate"), "resource_metadata")

	var resourceMeta struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	s.decode(s.do(http.MethodGet,
		s.site.URL+"/.well-known/oauth-protected-resource/mcp", nil, nil), &resourceMeta)
	s.Require().Equal([]string{s.site.URL}, resourceMeta.AuthorizationServers)

	var found endpoints
	s.decode(s.do(http.MethodGet,
		s.site.URL+"/.well-known/oauth-authorization-server", nil, nil), &found)
	s.Require().NotEmpty(found.Registration)
	return found
}

func (s *PasswordSuite) register(at string) string {
	response := s.do(http.MethodPost, at, strings.NewReader(
		`{"client_name":"Test app","redirect_uris":["`+callback+`"],
		  "token_endpoint_auth_method":"client_secret_basic"}`), nil)
	s.Require().Equal(http.StatusCreated, response.StatusCode)
	var registered struct {
		ClientID   string `json:"client_id"`
		AuthMethod string `json:"token_endpoint_auth_method"`
	}
	s.decode(response, &registered)
	s.Equal("none", registered.AuthMethod, "every client is public")
	return registered.ClientID
}

// pkce is a verifier and its S256 challenge.
func pkce() (string, string) {
	verifier := rand.Text() + rand.Text()
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorize starts a sign-in and returns the sign-in page's URL. It sends
// no scope, as an MCP client told none may.
func (s *PasswordSuite) authorize(at, clientID, challenge string) string {
	query := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {callback},
		"state": {"state-1"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"resource": {s.site.URL + "/mcp"},
	}
	response := s.do(http.MethodGet, at+"?"+query.Encode(), nil, nil)
	s.Require().Equal(http.StatusFound, response.StatusCode, s.readAll(response))
	location := response.Header.Get("Location")
	s.Require().Contains(location, "/login?authRequestID=")
	return s.site.URL + location
}

func (s *PasswordSuite) signIn(page, username, secret string) *http.Response {
	id := strings.TrimPrefix(page, s.site.URL+"/login?authRequestID=")
	id, err := url.QueryUnescape(id)
	s.Require().NoError(err)
	form := url.Values{"authRequestID": {id}, "username": {username}, "password": {secret}}
	return s.do(http.MethodPost, s.site.URL+"/login", strings.NewReader(form.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

// code follows a successful sign-in back through the provider to the
// client's redirect, and returns the code it carries.
func (s *PasswordSuite) code(signedIn *http.Response) string {
	s.Require().Equal(http.StatusFound, signedIn.StatusCode, s.readAll(signedIn))
	next := signedIn.Header.Get("Location")
	if !strings.HasPrefix(next, "http") {
		next = s.site.URL + next
	}
	back := s.do(http.MethodGet, next, nil, nil)
	s.Require().Equal(http.StatusFound, back.StatusCode, s.readAll(back))
	redirect, err := url.Parse(back.Header.Get("Location"))
	s.Require().NoError(err)
	s.Require().Equal("127.0.0.1:9", redirect.Host)
	s.Require().Equal("state-1", redirect.Query().Get("state"))
	return redirect.Query().Get("code")
}

// tokens is the token endpoint's answer.
type tokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Error   string `json:"error"`
}

func (s *PasswordSuite) exchange(at string, form url.Values) (int, tokens) {
	response := s.do(http.MethodPost, at, strings.NewReader(form.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	var answer tokens
	s.decode(response, &answer)
	return response.StatusCode, answer
}

func (s *PasswordSuite) callMCP(access string) (int, string) {
	response := s.do(http.MethodPost, s.site.URL+"/mcp", nil,
		map[string]string{"Authorization": "Bearer " + access})
	return response.StatusCode, s.readAll(response)
}

// signedIn runs the whole flow and returns the tokens and the endpoints.
func (s *PasswordSuite) signedIn() (endpoints, string, tokens) {
	found := s.discover()
	clientID := s.register(found.Registration)
	verifier, challenge := pkce()
	page := s.authorize(found.Authorization, clientID, challenge)

	shown := s.do(http.MethodGet, page, nil, nil)
	s.Require().Equal(http.StatusOK, shown.StatusCode)
	body := s.readAll(shown)
	s.Contains(body, "Test app", "the page names the app asking")
	s.Contains(body, "127.0.0.1:9", "and where the code goes")

	status, issued := s.exchange(found.Token, url.Values{
		"grant_type": {
			"authorization_code",
		},
		"code":          {s.code(s.signIn(page, "alice", alicePassword))},
		"redirect_uri":  {callback},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	})
	s.Require().Equal(http.StatusOK, status, issued.Error)
	s.Require().NotEmpty(issued.Access)
	s.Require().NotEmpty(issued.Refresh, "a refresh token, though the client asked for no scope")
	return found, clientID, issued
}

func (s *PasswordSuite) TestAClientSignsInAndReadsItsOwnLibrary() {
	_, _, issued := s.signedIn()
	status, user := s.callMCP(issued.Access)
	s.Equal(http.StatusOK, status)
	s.Equal(s.aliceID, user)
}

func (s *PasswordSuite) TestAWrongPasswordStaysOnThePage() {
	found := s.discover()
	_, challenge := pkce()
	page := s.authorize(found.Authorization, s.register(found.Registration), challenge)
	refused := s.signIn(page, "alice", "not the password at all")
	s.Equal(http.StatusUnauthorized, refused.StatusCode)
	s.Contains(s.readAll(refused), "wrong username or password")
}

// The code is useless without the verifier only the client holds.
func (s *PasswordSuite) TestACodeWithoutTheVerifierIsRefused() {
	found := s.discover()
	clientID := s.register(found.Registration)
	_, challenge := pkce()
	page := s.authorize(found.Authorization, clientID, challenge)
	status, _ := s.exchange(found.Token, url.Values{
		"grant_type": {
			"authorization_code",
		},
		"code":          {s.code(s.signIn(page, "alice", alicePassword))},
		"redirect_uri":  {callback},
		"client_id":     {clientID},
		"code_verifier": {"guessed"},
	})
	s.Equal(http.StatusBadRequest, status)
}

// A refresh token works once: using it issues a new one and spends it.
func (s *PasswordSuite) TestARefreshTokenRotates() {
	found, clientID, issued := s.signedIn()
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {issued.Refresh},
		"client_id": {clientID}}
	status, renewed := s.exchange(found.Token, refresh)
	s.Require().Equal(http.StatusOK, status, renewed.Error)
	s.NotEqual(issued.Refresh, renewed.Refresh)

	status, _ = s.exchange(found.Token, refresh)
	s.Equal(http.StatusBadRequest, status, "the spent token is refused")
}

// Removing an account stops its tokens at once, not when they expire.
func (s *PasswordSuite) TestARemovedAccountsTokensStopWorking() {
	found, clientID, issued := s.signedIn()
	s.Require().NoError(s.admin.Remove(s.T().Context(), "alice"))

	status, _ := s.callMCP(issued.Access)
	s.Equal(http.StatusForbidden, status)
	status, _ = s.exchange(found.Token, url.Values{"grant_type": {"refresh_token"},
		"refresh_token": {issued.Refresh}, "client_id": {clientID}})
	s.Equal(http.StatusBadRequest, status)
}

// Registration takes https anywhere and http only to this machine.
func (s *PasswordSuite) TestRegistrationRefusesARedirectThatLeaksTheCode() {
	found := s.discover()
	for _, uri := range []string{"http://evil.example/callback", "javascript:alert(1)", "/relative"} {
		response := s.do(http.MethodPost, found.Registration,
			strings.NewReader(`{"redirect_uris":["`+uri+`"]}`), nil)
		s.Equal(http.StatusBadRequest, response.StatusCode, uri)
	}
}

// The MCP Go SDK's own client discovers and registers against the server,
// so the documents pass a client written to the specification, not only
// this suite's reading of it.
func (s *PasswordSuite) TestTheSDKsClientDiscoversAndRegisters() {
	ctx := s.T().Context()
	meta, err := oauthex.GetAuthServerMeta(ctx,
		s.site.URL+"/.well-known/oauth-authorization-server", s.site.URL, s.site.Client())
	s.Require().NoError(err)
	s.Contains(meta.CodeChallengeMethodsSupported, "S256")
	s.Contains(meta.TokenEndpointAuthMethodsSupported, "none")

	registered, err := oauthex.RegisterClient(ctx, meta.RegistrationEndpoint,
		&oauthex.ClientRegistrationMetadata{
			RedirectURIs:            []string{callback},
			TokenEndpointAuthMethod: "none",
			ClientName:              "SDK client",
		}, s.site.Client())
	s.Require().NoError(err)
	s.NotEmpty(registered.ClientID)
}
