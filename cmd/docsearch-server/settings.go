package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	altsrc "github.com/urfave/cli-altsrc/v3"
	altyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/service/settings"
)

// configPath is the file --config names, read by every flag's file source
// once the flag itself has been read.
var configPath string

// Flags and keys that share a spelling.
const (
	flagAddr       = "addr"
	flagDSN        = "dsn"
	flagPublicURL  = "public-url"
	flagOIDCIssuer = "oidc-issuer"
	flagClaimName  = "oidc-required-claim-name"
	flagClaimValue = "oidc-required-claim-value"
	flagSignUp     = "oidc-sign-up"
)

// fileKeys are where each setting lives in docsearch.yaml. A flag missing
// here has no file key: a secret, which comes from the environment or the
// command line alone. The database DSN is one, because it carries the
// request role's password and the file is shown on the Sign-in page.
var fileKeys = map[string]string{
	flagAddr:                flagAddr,
	"auth":                  "auth.mode",
	"root":                  "roots",
	"allowed-origins":       "allowed_origins",
	"allow-public-bind":     "allow_public_bind",
	"shared-paths":          "shared_paths",
	flagPublicURL:           "public_url",
	"ui-client-id":          "ui.client_id",
	"instance-label":        "ui.instance_label",
	"upload-max-mb":         "upload_max_mb",
	flagOIDCIssuer:          "auth.oidc.issuer",
	"oidc-resource":         "auth.oidc.resource",
	"oidc-audience":         "auth.oidc.audience",
	"oidc-scopes-supported": "auth.oidc.scopes_supported",
	"oidc-required-scopes":  "auth.oidc.required_scopes",
	"oidc-client-id":        "auth.oidc.client_id",
	"oidc-default-owner":    "auth.oidc.default_owner",
	flagClaimName:           "auth.oidc.required_claim.name",
	flagClaimValue:          "auth.oidc.required_claim.value",
	flagSignUp:              "auth.oidc.sign_up",
}

// editable are the settings an administrator changes from the library UI:
// how people sign in, short of the sign-in mode itself, whose wrong value
// would lock everyone out.
var editable = map[string]settings.Editable{
	flagPublicURL:  {Key: fileKeys[flagPublicURL], URL: true},
	flagOIDCIssuer: {Key: fileKeys[flagOIDCIssuer], URL: true},
	flagClaimName:  {Key: fileKeys[flagClaimName]},
	flagClaimValue: {Key: fileKeys[flagClaimValue]},
	flagSignUp:     {Key: fileKeys[flagSignUp], Boolean: true},
}

// applyStored takes what the database keeps for a deployment with no
// docsearch.yaml, for every editable setting nothing stronger set.
func applyStored(cfg *config.Config, stored map[string]string) {
	for flag, e := range editable {
		value, kept := stored[e.Key]
		origin := cfg.Origins[flag]
		if !kept || origin.Kind != config.FromDefault {
			continue
		}
		switch flag {
		case flagPublicURL:
			cfg.PublicURL = value
		case flagOIDCIssuer:
			cfg.OIDC.Issuer = value
		case flagClaimName:
			cfg.OIDC.RequiredClaimName = value
		case flagClaimValue:
			cfg.OIDC.RequiredClaimValue = value
		case flagSignUp:
			cfg.OIDC.SignUp = value == "true"
		}
		cfg.Origins[flag] = config.Origin{Kind: config.FromDatabase, Where: "database", Key: e.Key}
	}
}

// from is where a flag is read when the command line leaves it out: the
// environment first, then docsearch.yaml. The environment wins, so an
// operator overrides the deployment's file without editing it.
func from(flag, env string) cli.ValueSourceChain {
	file := altyaml.YAML(fileKeys[flag], localConfig{})
	return cli.NewValueSourceChain(cli.EnvVar(env), file)
}

// localConfig names the config file to altsrc, and names nothing for a path
// that is a URL: altsrc fetches one over HTTP, and the server reads its
// settings from a file it was handed, never from the network. checkConfig
// then refuses the path.
type localConfig struct{}

func (localConfig) SourceURI() string {
	if remote(configPath) {
		return ""
	}
	return configPath
}

// remote reports whether a config path is a URL rather than a file.
func remote(path string) bool { return strings.Contains(path, "://") }

// rootsFrom is from for --root, whose list the file holds as a YAML list.
// altsrc joins a list with commas, and --root splits on the platform's
// path-list separator, so the file's list is joined with that instead.
func rootsFrom() cli.ValueSourceChain {
	return cli.NewValueSourceChain(cli.EnvVar(config.EnvRoot), rootsSource{})
}

// rootsSource reads the roots list from docsearch.yaml.
type rootsSource struct{}

func (rootsSource) Lookup() (string, bool) {
	tree, err := readConfig(configPath)
	if err != nil || tree == nil {
		return "", false
	}
	value, ok := altsrc.NestedVal(fileKeys["root"], tree)
	if !ok {
		return "", false
	}
	list, ok := value.([]any)
	if !ok {
		return fmt.Sprint(value), true
	}
	roots := make([]string, 0, len(list))
	for _, root := range list {
		roots = append(roots, fmt.Sprint(root))
	}
	return strings.Join(roots, string(filepath.ListSeparator)), true
}

func (rootsSource) String() string {
	return fmt.Sprintf("yaml file %q at key %q", configPath, fileKeys["root"])
}

func (s rootsSource) GoString() string { return s.String() }

// readConfig parses docsearch.yaml, or answers nothing when no file is named.
func readConfig(path string) (map[any]any, error) {
	if path == "" || remote(path) {
		return nil, nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, fmt.Errorf("read the config file: %w", err)
	}
	tree := map[any]any{}
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return tree, nil
}

// errUnknownKeys is a config file holding a key no setting reads, which is
// almost always a typo that would otherwise leave a setting at its default.
var errUnknownKeys = errors.New("keys no setting reads")

// checkConfig refuses a config file that is missing, does not parse, or
// holds keys no setting reads. altsrc skips such a file in silence, which
// would start the server on defaults the operator never chose.
func checkConfig(path string) error {
	if path == "" {
		return nil
	}
	if remote(path) {
		return fmt.Errorf("--config names a file, not a URL: %s", path)
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return fmt.Errorf("the config file %s is not a file docsearch can read", path)
	}
	tree, err := readConfig(path)
	if err != nil {
		return err
	}
	if unknown := unknownKeys(tree); len(unknown) > 0 {
		return fmt.Errorf("%s: %w: %s", path, errUnknownKeys, strings.Join(unknown, ", "))
	}
	return nil
}

// unknownKeys are the keys in tree no setting reads, sorted.
func unknownKeys(tree map[any]any) []string {
	known := make([]string, 0, len(fileKeys))
	for _, key := range fileKeys {
		known = append(known, key)
	}
	var unknown []string
	for _, key := range leaves("", tree) {
		if !slices.Contains(known, key) {
			unknown = append(unknown, key)
		}
	}
	slices.Sort(unknown)
	return unknown
}

// leaves are the dotted keys of every value in tree, so auth.oidc.issuer
// for a nested map.
func leaves(prefix string, tree map[any]any) []string {
	var out []string
	for k, v := range tree {
		key := fmt.Sprint(k)
		if prefix != "" {
			key = prefix + "." + key
		}
		if child, ok := mapOf(v); ok {
			out = append(out, leaves(key, child)...)
			continue
		}
		out = append(out, key)
	}
	return out
}

// mapOf is v as a map, whichever key type the YAML decoder gave it.
func mapOf(v any) (map[any]any, bool) {
	switch child := v.(type) {
	case map[any]any:
		return child, true
	case map[string]any:
		converted := make(map[any]any, len(child))
		for k, cv := range child {
			converted[k] = cv
		}
		return converted, true
	default:
		return nil, false
	}
}

// origins are where every setting came from, keyed by flag name.
func origins(cmd *cli.Command, args []string) map[string]config.Origin {
	out := make(map[string]config.Origin, len(fileKeys))
	for _, flag := range cmd.Flags {
		name := flag.Names()[0]
		if _, held := fileKeys[name]; !held {
			continue
		}
		env := ""
		if documented, ok := flag.(cli.DocGenerationFlag); ok {
			if envs := documented.GetEnvVars(); len(envs) > 0 {
				env = envs[0]
			}
		}
		out[name] = originOf(cmd, name, env, args)
	}
	return out
}

// originOf is where a setting's value came from: the command line, the
// environment, docsearch.yaml, or the default.
func originOf(cmd *cli.Command, flag, env string, args []string) config.Origin {
	key := fileKeys[flag]
	if onCommandLine(flag, args) {
		return config.Origin{Kind: config.FromFlag, Where: "--" + flag, Key: key}
	}
	if _, set := os.LookupEnv(env); set && env != "" {
		return config.Origin{Kind: config.FromEnv, Where: env, Key: key}
	}
	if cmd.IsSet(flag) && configPath != "" {
		return config.Origin{Kind: config.FromFile, Where: configPath, Key: key}
	}
	return config.Origin{Kind: config.FromDefault, Key: key}
}

// onCommandLine reports whether args set flag, as --flag, --flag=value or
// the single-dash spellings urfave also takes.
func onCommandLine(flag string, args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		name := strings.TrimLeft(arg, "-")
		if name == arg {
			continue
		}
		if before, _, _ := strings.Cut(name, "="); before == flag {
			return true
		}
	}
	return false
}
