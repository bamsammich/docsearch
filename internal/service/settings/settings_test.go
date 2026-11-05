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

// With a file, nothing is stored: the change comes back as the whole file,
// comments kept, and the changed line named.
func (s *SettingsSuite) TestWithAFileAChangeBecomesTheUpdatedFile() {
	result, err := s.withFile(nil).Update(s.T().Context(),
		[]settings.Change{{Flag: "oidc-required-claim-value", Value: "docsearch-users"}})
	s.Require().NoError(err)

	s.False(result.Saved)
	s.Contains(result.File, "# Deployed by the platform team.")
	s.Contains(result.File, "value: docsearch-users # who may sign in")
	s.Equal([]int{8}, result.Changed)
}

func (s *SettingsSuite) TestAKeyTheFileLacksIsAddedWhereItBelongs() {
	result, err := s.withFile(nil).Update(s.T().Context(),
		[]settings.Change{
			{Flag: "oidc-sign-up", Value: "true"},
			{Flag: "public-url", Value: "https://docs.example.com"},
		})
	s.Require().NoError(err)

	s.Contains(result.File, "    sign_up: true\n")
	s.Contains(result.File, "public_url: https://docs.example.com\n")
	s.Len(result.Changed, 2)
}

func (s *SettingsSuite) TestAnEmptyValueTakesTheKeyOut() {
	result, err := s.withFile(nil).Update(s.T().Context(),
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

func (s *SettingsSuite) TestStoredIsByFlagAndEmptyWithAFile() {
	s.store.EXPECT().
		Stored(mock.Anything).
		Return(map[string]string{"auth.oidc.sign_up": "true"}, nil)

	stored, err := s.withDatabase(nil).Stored(s.T().Context())
	s.Require().NoError(err)
	s.Equal(map[string]string{"oidc-sign-up": "true"}, stored)

	stored, err = s.withFile(nil).Stored(s.T().Context())
	s.Require().NoError(err)
	s.Empty(stored)
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
