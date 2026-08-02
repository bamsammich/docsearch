// Package config resolves server configuration from flags and environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the fully resolved server configuration.
type Config struct {
	Addr            string
	DBPath          string
	LibraryRoot     string
	BearerToken     string
	AllowedOrigins  []string
	AllowPublicBind bool
	RecentJobWindow int
}

const (
	EnvToken   = "DOCSEARCH_TOKEN" //nolint:gosec // name of a variable, not a credential
	EnvDB      = "DOCSEARCH_DB"
	EnvRoot    = "DOCSEARCH_ROOT"
	EnvOrigins = "DOCSEARCH_ALLOWED_ORIGINS"
	EnvAddr    = "DOCSEARCH_ADDR"
)

// ErrPublicBind is returned when the configured address is not loopback and
// the operator has not explicitly opted in.
var ErrPublicBind = errors.New(
	"refusing to bind a public address without --allow-public-bind: the tool surface " +
		"includes a filesystem path parameter and this service must not be reachable " +
		"from outside a trusted network")

// Validate resolves derived values and rejects unsafe combinations.
func (c *Config) Validate() error {
	if c.BearerToken == "" {
		return fmt.Errorf("no bearer token: set %s", EnvToken)
	}
	if c.DBPath == "" {
		return errors.New("no database path: set --db or " + EnvDB)
	}
	if c.LibraryRoot == "" {
		return errors.New("no library root: set --root or " + EnvRoot)
	}

	// The library root is resolved once, at startup, through symlinks. Every
	// later path check compares against this value, so a symlinked root does
	// not silently widen what the tool surface will accept.
	root, err := filepath.EvalSymlinks(c.LibraryRoot)
	if err != nil {
		return fmt.Errorf("library root %q is not usable: %w", c.LibraryRoot, err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("library root %q: %w", c.LibraryRoot, err)
	}
	c.LibraryRoot = abs

	if !c.AllowPublicBind && !IsLoopbackAddr(c.Addr) {
		return ErrPublicBind
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

// FromEnv fills unset fields from the environment.
func (c *Config) FromEnv() {
	if c.BearerToken == "" {
		c.BearerToken = os.Getenv(EnvToken)
	}
	if c.DBPath == "" {
		c.DBPath = os.Getenv(EnvDB)
	}
	if c.LibraryRoot == "" {
		c.LibraryRoot = os.Getenv(EnvRoot)
	}
	if c.Addr == "" {
		if v := os.Getenv(EnvAddr); v != "" {
			c.Addr = v
		} else {
			c.Addr = "127.0.0.1:8765"
		}
	}
	if len(c.AllowedOrigins) == 0 {
		if v := os.Getenv(EnvOrigins); v != "" {
			for _, o := range strings.Split(v, ",") {
				if o = strings.TrimSpace(o); o != "" {
					c.AllowedOrigins = append(c.AllowedOrigins, o)
				}
			}
		}
	}
}
