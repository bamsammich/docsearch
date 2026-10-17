package signed_test

import (
	"crypto"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/resource"
	"github.com/bamsammich/docsearch/internal/resource/resourcetest"
	"github.com/bamsammich/docsearch/internal/resource/signed"
)

const (
	issuer    = "https://issuer.example"
	docsearch = "https://docs.example.com/mcp"
)

// SignedSuite verifies real signatures against go-oidc's own static key set,
// so the algorithms docsearch accepts are tested without an issuer to fetch
// keys from.
type SignedSuite struct {
	suite.Suite
	key    *rsa.PrivateKey
	reader *signed.Reader
}

func TestSigned(t *testing.T) { suite.Run(t, new(SignedSuite)) }

func (s *SignedSuite) SetupTest() {
	s.key = resourcetest.NewKey(s.T())
	s.reader = signed.New(issuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&s.key.PublicKey}})
}

func (s *SignedSuite) sign(claims map[string]any) string {
	return resourcetest.Sign(s.T(), s.key, claims)
}

// minted is a token's claims as the issuer would mint them for docsearch.
func minted() map[string]any {
	return map[string]any{
		"iss":   issuer,
		"sub":   "subject-1",
		"aud":   []string{docsearch},
		"scope": "openid docs:read",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
}

func (s *SignedSuite) TestReadsWhatTheIssuerSigned() {
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	claims := minted()
	claims["exp"] = expiry.Unix()

	claims["groups"] = []string{"docsearch-users"}

	got, err := s.reader.Read(s.T().Context(), s.sign(claims))
	s.Require().NoError(err)
	// A claim docsearch has no field for still arrives, for a policy to read.
	s.Equal([]any{"docsearch-users"}, got.Stated["groups"])
	got.Stated = nil
	s.Equal(resource.Claims{
		Issuer:   issuer,
		Subject:  "subject-1",
		Audience: []string{docsearch},
		Scopes:   []string{"openid", "docs:read"},
		Expiry:   expiry,
	}, got)
}

// The audience is not this package's check: a token for another resource
// reads cleanly here and is refused by package resource, which knows which
// audience the deployment expects.
func (s *SignedSuite) TestLeavesTheAudienceToTheResourceServer() {
	claims := minted()
	claims["aud"] = "https://elsewhere.example"
	got, err := s.reader.Read(s.T().Context(), s.sign(claims))
	s.Require().NoError(err)
	s.Equal([]string{"https://elsewhere.example"}, got.Audience)
}

func (s *SignedSuite) TestRefusesWhatTheIssuerDidNotSign() {
	cases := map[string]string{
		"a key the issuer never published": resourcetest.Sign(
			s.T(), resourcetest.NewKey(s.T()), minted()),
		// The algorithm-confusion attack: an HMAC keyed with something an
		// attacker can know. Only asymmetric algorithms are accepted.
		"a symmetric algorithm": resourcetest.SignWith(s.T(), jose.SigningKey{
			Algorithm: jose.HS256, Key: []byte("a secret anyone could choose, 32b"),
		}, minted()),
	}
	for name, token := range cases {
		s.Run(name, func() {
			_, err := s.reader.Read(s.T().Context(), token)
			s.ErrorIs(err, auth.ErrInvalidToken)
		})
	}
}

func (s *SignedSuite) TestRefusesATokenPastItsExpiry() {
	claims := minted()
	claims["exp"] = time.Now().Add(-time.Minute).Unix()
	_, err := s.reader.Read(s.T().Context(), s.sign(claims))
	s.ErrorIs(err, auth.ErrInvalidToken)
}

func (s *SignedSuite) TestRefusesAnotherIssuersClaim() {
	claims := minted()
	claims["iss"] = "https://elsewhere.example"
	_, err := s.reader.Read(s.T().Context(), s.sign(claims))
	s.ErrorIs(err, auth.ErrInvalidToken)
}
