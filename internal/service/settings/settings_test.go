package settings_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/service/settings"
	"github.com/bamsammich/docsearch/internal/service/settings/mocks"
)

// editable are the settings these cases change.
var editable = map[string]settings.Editable{
	"oidc-required-claim-value": {Key: "auth.oidc.required_claim.value"},
	"oidc-sign-up":              {Key: "auth.oidc.sign_up", Boolean: true},
	"public-url":                {Key: "public_url", URL: true},
}

const deployed = `# Deployed by the platform team.
auth:
  mode: oidc
  oidc:
    issuer: https://id.example.com
    required_claim:
      name: groups
      value: staff # who may sign in
`

type SettingsSuite struct {
	suite.Suite
	store *mocks.MockStore
}

func TestSettings(t *testing.T) { suite.Run(t, new(SettingsSuite)) }

func (s *SettingsSuite) SetupTest() { s.store = mocks.NewMockStore(s.T()) }

func (s *SettingsSuite) withFile(origins map[string]config.Origin) *settings.Service {
	return settings.New(editable, origins, "/etc/docsearch.yaml", s.store,
		func(string) ([]byte, error) { return []byte(deployed), nil })
}

func (s *SettingsSuite) withDatabase(origins map[string]config.Origin) *settings.Service {
	return settings.New(editable, origins, "", s.store,
		func(string) ([]byte, error) { return nil, errors.New("no file") })
}

// fromFile are the settings the deployed file holds.
var fromFile = map[string]config.Origin{
	"oidc-required-claim-value": {Kind: config.FromFile, Where: "/etc/docsearch.yaml",
		Key: "auth.oidc.required_claim.value"},
}

// A setting the file holds is changed in the file: the change comes back
// as the whole file, comments kept and the changed line named, and nothing
// is stored.
func (s *SettingsSuite) TestASettingTheFileHoldsBecomesTheUpdatedFile() {
	result, err := s.withFile(fromFile).Update(s.T().Context(),
		[]settings.Change{{Flag: "oidc-required-claim-value", Value: "docsearch-users"}})
	s.Require().NoError(err)

	s.False(result.Saved)
	s.Contains(result.File, "# Deployed by the platform team.")
	s.Contains(result.File, "value: docsearch-users # who may sign in")
	s.Equal([]int{8}, result.Changed)
}

// The database sits beneath the file, so a setting the file leaves out is
// kept there even when there is a file.
func (s *SettingsSuite) TestASettingTheFileLeavesOutIsKept() {
	s.store.EXPECT().Set(mock.Anything, "auth.oidc.sign_up", mock.Anything).Return(nil)

	result, err := s.withFile(fromFile).Update(s.T().Context(),
		[]settings.Change{{Flag: "oidc-sign-up", Value: "true"}})
	s.Require().NoError(err)
	s.True(result.Saved)
	s.Empty(result.File)
}

func (s *SettingsSuite) TestAChangeToBothIsSplitBetweenThem() {
	s.store.EXPECT().Set(mock.Anything, "public_url", mock.Anything).Return(nil)

	result, err := s.withFile(fromFile).Update(s.T().Context(), []settings.Change{
		{Flag: "oidc-required-claim-value", Value: "docsearch-users"},
		{Flag: "public-url", Value: "https://docs.example.com"},
	})
	s.Require().NoError(err)
	s.True(result.Saved)
	s.Contains(result.File, "docsearch-users")
	s.NotContains(result.File, "public_url", "a kept setting is not written into the file")
}

func (s *SettingsSuite) TestAnEmptyValueTakesTheKeyOut() {
	result, err := s.withFile(fromFile).Update(s.T().Context(),
		[]settings.Change{{Flag: "oidc-required-claim-value", Value: ""}})
	s.Require().NoError(err)
	s.NotContains(result.File, "staff")
}

// What the environment sets would never take effect from the file.
func (s *SettingsSuite) TestASettingTheEnvironmentSetsIsRefused() {
	origins := map[string]config.Origin{
		"public-url": {Kind: config.FromEnv, Where: config.EnvPublicURL},
	}
	_, err := s.withDatabase(origins).Update(s.T().Context(),
		[]settings.Change{{Flag: "public-url", Value: "https://docs.example.com"}})
	s.Require().ErrorIs(err, settings.ErrLocked)
	s.ErrorContains(err, config.EnvPublicURL)
}

func (s *SettingsSuite) TestASettingTheUIDoesNotChangeIsRefused() {
	_, err := s.withDatabase(nil).
		Update(s.T().Context(), []settings.Change{{Flag: "dsn", Value: "x"}})
	s.Require().ErrorIs(err, settings.ErrNotEditable)
}

func (s *SettingsSuite) TestWithoutAFileAChangeIsKept() {
	s.store.EXPECT().Set(mock.Anything, "auth.oidc.sign_up", mock.MatchedBy(func(v *string) bool {
		return v != nil && *v == "true"
	})).Return(nil)
	s.store.EXPECT().Set(mock.Anything, "public_url", (*string)(nil)).Return(nil)

	result, err := s.withDatabase(nil).Update(s.T().Context(), []settings.Change{
		{Flag: "oidc-sign-up", Value: "true"}, {Flag: "public-url", Value: ""},
	})
	s.Require().NoError(err)
	s.True(result.Saved)
	s.Empty(result.File)
}

func (s *SettingsSuite) TestABooleanTakesTrueOrFalse() {
	_, err := s.withDatabase(nil).
		Update(s.T().Context(), []settings.Change{{Flag: "oidc-sign-up", Value: "yes"}})
	s.Require().Error(err)
}

func (s *SettingsSuite) TestStoredIsByFlagWithOrWithoutAFile() {
	s.store.EXPECT().
		Stored(mock.Anything).
		Return(map[string]string{"auth.oidc.sign_up": "true"}, nil)

	for _, service := range []*settings.Service{s.withDatabase(nil), s.withFile(fromFile)} {
		stored, err := service.Stored(s.T().Context())
		s.Require().NoError(err)
		s.Equal(map[string]string{"oidc-sign-up": "true"}, stored)
	}
}

// A value that would stop the server from starting is refused before it is
// kept or written.
func (s *SettingsSuite) TestAURLSettingTakesOnlyAnHTTPURL() {
	for _, bad := range []string{"not a url", "javascript://x/%0aalert(1)", "docs.example.com"} {
		_, err := s.withDatabase(nil).
			Update(s.T().Context(), []settings.Change{{Flag: "public-url", Value: bad}})
		s.Require().ErrorIs(err, settings.ErrInvalid, bad)
	}
}

// RewriteSuite holds how a change lands in docsearch.yaml: a value changed
// in place leaves every other byte as the operator wrote it.
type RewriteSuite struct{ suite.Suite }

func TestRewrite(t *testing.T) { suite.Run(t, new(RewriteSuite)) }

const opsFile = `# Deployed from the ops repository.
addr: 0.0.0.0:8765

auth:
  mode: oidc    # set by the platform team
  oidc:
    issuer: "https://id.example.com"
    sign_up: false
`

func (s *RewriteSuite) TestAValueChangedInPlaceKeepsEveryOtherByte() {
	out, err := settings.Rewrite([]byte(opsFile), map[string]any{
		"auth.oidc.issuer":  "https://login.example.com",
		"auth.oidc.sign_up": true,
	})
	s.Require().NoError(err)
	s.Equal(`# Deployed from the ops repository.
addr: 0.0.0.0:8765

auth:
  mode: oidc    # set by the platform team
  oidc:
    issuer: https://login.example.com
    sign_up: true
`, string(out))
}

func (s *RewriteSuite) TestAValueKeepsItsComment() {
	out, err := settings.Rewrite([]byte(opsFile), map[string]any{"auth.mode": "password"})
	s.Require().NoError(err)
	s.Contains(string(out), "  mode: password    # set by the platform team\n")
	s.Contains(string(out), "\n\nauth:", "the blank line stays")
}

func (s *RewriteSuite) TestAValueThatNeedsQuotesIsQuoted() {
	out, err := settings.Rewrite([]byte(opsFile), map[string]any{"addr": "# not a comment"})
	s.Require().NoError(err)
	s.Contains(string(out), "addr: '# not a comment'\n")
}

func (s *RewriteSuite) TestANewKeyWritesTheDocumentOut() {
	out, err := settings.Rewrite([]byte(opsFile),
		map[string]any{"public_url": "https://docs.example.com"})
	s.Require().NoError(err)
	s.Contains(string(out), "public_url: https://docs.example.com")
	s.Contains(string(out), "# Deployed from the ops repository.")
}
