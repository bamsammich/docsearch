// Package config resolves server configuration from flags and environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bamsammich/docsearch/internal/owner"
)

// Config is the fully resolved server configuration.
type Config struct {
	Addr string
	// LibraryRoots are the directories add_document will accept a path
	// inside. Which directories are ingestable is an operator decision: no
	// tool parameter widens the set, and a caller cannot add to it.
	LibraryRoots []string
	// DSN addresses the Postgres database holding every library.
	DSN string
	// Auth is how a request's user is decided. Unset reads as token, which
	// is what every v1 install already runs.
	Auth            owner.Mode
	BearerToken     string
	AllowedOrigins  []string
	AllowPublicBind bool
	RecentJobWindow int
	// OIDC is read only in auth mode oidc.
	OIDC OIDC
}

// OIDC is how a deployment in auth mode oidc reaches its provider, and who
// it lets in.
type OIDC struct {
	// Issuer is the provider's issuer identifier.
	Issuer string
	// Resource is docsearch's /mcp URI as clients address it.
	Resource string
	// Audience is the aud value a token must carry, when the provider binds
	// one of its own instead of Resource.
	Audience string
	// ClientID and ClientSecret are docsearch's credentials at the provider,
	// needed only to introspect opaque tokens.
	ClientID     string
	ClientSecret string
	// DefaultOwner is the subject, at Issuer, who inherits the library the
	// deployment filled before it had accounts.
	DefaultOwner string
	// RequiredClaimName and RequiredClaimValue name a claim every token must
	// carry.
	RequiredClaimName  string
	RequiredClaimValue string
	ScopesSupported    []string
	RequiredScopes     []string
	// SignUp creates an account for an identity that has none.
	SignUp bool
}

const (
	EnvToken = "DOCSEARCH_TOKEN" //nolint:gosec // name of a variable, not a credential
	// EnvDSN addresses the Postgres database the server and the worker read.
	EnvDSN = "DOCSEARCH_DSN"
	// EnvDB is the SQLite file the migrate command still takes, which step
	// 4g of docs/plans/postgres-multiuser.md removes along with the dialect.
	EnvDB   = "DOCSEARCH_DB"
	EnvRoot = "DOCSEARCH_ROOT"
	// EnvAuth names the authentication mode.
	EnvAuth = "DOCSEARCH_AUTH"
	// EnvUser names the library a worker claims jobs from.
	EnvUser    = "DOCSEARCH_USER"
	EnvOrigins = "DOCSEARCH_ALLOWED_ORIGINS"
	EnvAddr    = "DOCSEARCH_ADDR"
	// EnvAllowPublicBind permits a non-loopback address, as
	// --allow-public-bind does.
	EnvAllowPublicBind = "DOCSEARCH_ALLOW_PUBLIC_BIND"
	// EnvServer is where docsearch finds docsearch-server. Every command but
	// migrate goes through it.
	EnvServer = "DOCSEARCH_SERVER"

	// The oidc mode's settings. The client secret is read from the
	// environment alone, as the token is.
	EnvOIDCIssuer             = "DOCSEARCH_OIDC_ISSUER"
	EnvOIDCResource           = "DOCSEARCH_OIDC_RESOURCE"
	EnvOIDCAudience           = "DOCSEARCH_OIDC_AUDIENCE"
	EnvOIDCScopesSupported    = "DOCSEARCH_OIDC_SCOPES_SUPPORTED"
	EnvOIDCRequiredScopes     = "DOCSEARCH_OIDC_REQUIRED_SCOPES"
	EnvOIDCClientID           = "DOCSEARCH_OIDC_CLIENT_ID"
	EnvOIDCClientSecret       = "DOCSEARCH_OIDC_CLIENT_SECRET" //nolint:gosec // name of a variable, not a credential
	EnvOIDCDefaultOwner       = "DOCSEARCH_OIDC_DEFAULT_OWNER"
	EnvOIDCSignUp             = "DOCSEARCH_OIDC_SIGN_UP"
	EnvOIDCRequiredClaimName  = "DOCSEARCH_OIDC_REQUIRED_CLAIM_NAME"
	EnvOIDCRequiredClaimValue = "DOCSEARCH_OIDC_REQUIRED_CLAIM_VALUE"
)

// ErrPublicBind is returned when the configured address is not loopback and
// the operator has not explicitly opted in.
var ErrPublicBind = errors.New(
	"refusing to bind a public address without --allow-public-bind: the tool surface " +
		"includes a filesystem path parameter and this service must not be reachable " +
		"from outside a trusted network")

// ErrOpenToTheNetwork is returned for auth mode none on an address that is
// not loopback.
//
// --allow-public-bind opts into reaching the server from elsewhere, not into
// serving a library to whoever arrives. Mode none asks for no credential at
// all, so the two together are an open document store, and no flag spells
// that intention clearly enough to act on.
var ErrOpenToTheNetwork = errors.New(
	"refusing auth mode none on an address that is not loopback: the mode asks for no " +
		"credential, so every request would be served. Bind loopback, or run mode token")

// Validate resolves derived values and rejects unsafe combinations.
func (c *Config) Validate() error {
	if c.Auth == 0 {
		c.Auth = owner.Token
	}
	if c.Auth == owner.Token && c.BearerToken == "" {
		return fmt.Errorf("no bearer token: set %s", EnvToken)
	}
	if c.Auth == owner.OIDC {
		if err := c.OIDC.validate(); err != nil {
			return err
		}
	}
	if c.DSN == "" {
		return errors.New("no database: set --dsn or " + EnvDSN)
	}
	if len(c.LibraryRoots) == 0 {
		return errors.New("no library root: set --root or " + EnvRoot)
	}

	// Each root is resolved once, at startup, through symlinks. Every later
	// path check compares against these values, so a symlinked root does not
	// silently widen what the tool surface will accept.
	//
	// One unusable root fails startup rather than being dropped: a server that
	// quietly serves three of the four directories an operator configured is
	// worse than one that will not start, because the missing one only
	// surfaces as an unexplained rejection much later.
	seen := make(map[string]bool, len(c.LibraryRoots))
	resolved := make([]string, 0, len(c.LibraryRoots))
	for _, root := range c.LibraryRoots {
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			return fmt.Errorf("library root %q is not usable: %w", root, err)
		}
		abs, err := filepath.Abs(real)
		if err != nil {
			return fmt.Errorf("library root %q: %w", root, err)
		}
		// A root must be a directory. A file passes both calls above, and
		// `within` treats a path equal to the root as inside it -- so naming
		// a file as a root quietly makes that one file ingestable, and
		// add_document then advertises "the path must be inside" it.
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("library root %q is not usable: %w", root, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("library root %q is not a directory", root)
		}
		// Two spellings of one directory are one root. Left in, they appear
		// twice in the tool description without meaning anything.
		if seen[abs] {
			continue
		}
		seen[abs] = true
		resolved = append(resolved, abs)
	}
	c.LibraryRoots = resolved

	if !c.AllowPublicBind && !IsLoopbackAddr(c.Addr) {
		return ErrPublicBind
	}
	if c.Auth == owner.None && !IsLoopbackAddr(c.Addr) {
		return ErrOpenToTheNetwork
	}
	return nil
}

// validate refuses an oidc configuration that could not work, at startup
// rather than on the first request.
func (o OIDC) validate() error {
	if o.Issuer == "" {
		return errors.New("auth mode oidc needs an issuer: set " + EnvOIDCIssuer)
	}
	if o.Resource == "" {
		return errors.New("auth mode oidc needs docsearch's own /mcp URI: set " + EnvOIDCResource)
	}
	if (o.ClientID == "") != (o.ClientSecret == "") {
		return fmt.Errorf(
			"introspection needs both %s and %s",
			EnvOIDCClientID,
			EnvOIDCClientSecret,
		)
	}
	if (o.RequiredClaimName == "") != (o.RequiredClaimValue == "") {
		return fmt.Errorf("a required claim needs both %s and %s",
			EnvOIDCRequiredClaimName, EnvOIDCRequiredClaimValue)
	}
	return nil
}

// IsLoopbackAddr reports whether addr binds only to a loopback interface.
// An empty host, "0.0.0.0" or "::" all bind every interface and are not
// loopback.
func IsLoopbackAddr(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	case "", "0.0.0.0", "::", "*":
		return false
	}
	// A specific non-loopback interface (a Tailscale address, say) is a
	// deliberate choice and still requires the explicit flag.
	return strings.HasPrefix(host, "127.")
}
