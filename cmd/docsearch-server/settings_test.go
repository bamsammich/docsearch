package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/config"
)

// SettingsSuite parses the real command against a docsearch.yaml, to hold
// the order a setting is taken in: the command line, the environment, the
// file, then the default.
type SettingsSuite struct {
	suite.Suite
	file string
}

func TestSettings(t *testing.T) { suite.Run(t, new(SettingsSuite)) }

func (s *SettingsSuite) SetupTest() {
	configPath = ""
	s.file = filepath.Join(s.T().TempDir(), "docsearch.yaml")
	s.write(`
addr: 127.0.0.1:9001
auth:
  mode: oidc
  oidc:
    issuer: https://id.example.com
    sign_up: true
    required_claim:
      name: groups
      value: docsearch-users
roots:
  - /srv/docs
  - /srv/more
ui:
  instance_label: From the file
upload_max_mb: 25
`)
	// The environment of whoever runs the tests must not leak in: every
	// variable a flag reads is cleared, and restored afterwards.
	for _, flag := range command().Flags {
		documented, ok := flag.(cli.DocGenerationFlag)
		if !ok {
			continue
		}
		for _, env := range documented.GetEnvVars() {
			s.T().Setenv(env, "")
			s.Require().NoError(os.Unsetenv(env))
		}
	}
}

func (s *SettingsSuite) write(body string) {
	s.Require().NoError(os.WriteFile(s.file, []byte(body), 0o600))
}

// parse runs the command on args and answers the configuration it built.
func (s *SettingsSuite) parse(args ...string) (config.Config, error) {
	var cfg config.Config
	var err error
	cmd := command()
	cmd.Action = func(_ context.Context, c *cli.Command) error {
		cfg, err = configFrom(c, args)
		return nil
	}
	s.Require().NoError(cmd.Run(s.T().Context(), append([]string{"docsearch-server"}, args...)))
	return cfg, err
}

func (s *SettingsSuite) TestTheFileSetsWhatNothingStrongerDoes() {
	cfg, err := s.parse("--config", s.file)
	s.Require().NoError(err)

	s.Equal("127.0.0.1:9001", cfg.Addr)
	s.Equal("oidc", cfg.Auth.String())
	s.Equal("https://id.example.com", cfg.OIDC.Issuer)
	s.True(cfg.OIDC.SignUp)
	s.Equal("groups", cfg.OIDC.RequiredClaimName)
	s.Equal([]string{"/srv/docs", "/srv/more"}, cfg.LibraryRoots)
	s.Equal("From the file", cfg.InstanceLabel)
	s.Equal(int64(25), cfg.UploadMaxMB)
	s.Equal(s.file, cfg.File)

	s.Equal(config.Origin{Kind: config.FromFile, Where: s.file, Key: "auth.oidc.issuer"},
		cfg.Origins["oidc-issuer"])
	s.Equal(config.FromDefault, cfg.Origins["public-url"].Kind)
}

func (s *SettingsSuite) TestTheEnvironmentOverridesTheFile() {
	s.T().Setenv(config.EnvOIDCIssuer, "https://other.example.com")

	cfg, err := s.parse("--config", s.file)
	s.Require().NoError(err)

	s.Equal("https://other.example.com", cfg.OIDC.Issuer)
	s.Equal(
		config.Origin{Kind: config.FromEnv, Where: config.EnvOIDCIssuer, Key: "auth.oidc.issuer"},
		cfg.Origins["oidc-issuer"],
	)
}

func (s *SettingsSuite) TestTheCommandLineOverridesBoth() {
	s.T().Setenv(config.EnvAddr, "127.0.0.1:9002")

	cfg, err := s.parse("--config", s.file, "--addr", "127.0.0.1:9003")
	s.Require().NoError(err)

	s.Equal("127.0.0.1:9003", cfg.Addr)
	s.Equal(config.FromFlag, cfg.Origins["addr"].Kind)
}

func (s *SettingsSuite) TestTheFileCanBeNamedInTheEnvironment() {
	s.T().Setenv(config.EnvConfig, s.file)

	cfg, err := s.parse()
	s.Require().NoError(err)
	s.Equal("127.0.0.1:9001", cfg.Addr)
}

// A typo would otherwise leave the setting at its default in silence.
func (s *SettingsSuite) TestAKeyNoSettingReadsIsRefused() {
	s.write("auth:\n  oidc:\n    isuer: https://id.example.com\n")

	_, err := s.parse("--config", s.file)
	s.Require().ErrorIs(err, errUnknownKeys)
	s.ErrorContains(err, "auth.oidc.isuer")
}

func (s *SettingsSuite) TestAFileThatIsNotThereIsRefused() {
	_, err := s.parse("--config", filepath.Join(s.T().TempDir(), "missing.yaml"))
	s.Require().ErrorContains(err, "not a file docsearch can read")
}

func (s *SettingsSuite) TestWithNoFileEverySettingIsTheDefaultOrTheEnvironment() {
	cfg, err := s.parse()
	s.Require().NoError(err)

	s.Empty(cfg.File)
	s.Equal("127.0.0.1:8765", cfg.Addr)
	s.Equal(config.FromDefault, cfg.Origins["addr"].Kind)
}

// Without a file, what the database keeps fills a setting nothing stronger
// set, and leaves alone one the environment set.
func (s *SettingsSuite) TestStoredSettingsFillOnlyWhatNothingStrongerSet() {
	s.T().Setenv(config.EnvPublicURL, "https://env.example.com")

	cfg, err := s.parse()
	s.Require().NoError(err)
	applyStored(&cfg, map[string]string{
		"public_url":        "https://stored.example.com",
		"auth.oidc.sign_up": "true",
	})

	s.Equal("https://env.example.com", cfg.PublicURL)
	s.Equal(config.FromEnv, cfg.Origins["public-url"].Kind)
	s.True(cfg.OIDC.SignUp)
	s.Equal(config.FromDatabase, cfg.Origins["oidc-sign-up"].Kind)
}
