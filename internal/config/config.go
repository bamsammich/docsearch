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
	Addr string
	// LibraryRoots are the directories add_document will accept a path
	// inside. Which directories are ingestable is an operator decision: no
	// tool parameter widens the set, and a caller cannot add to it.
	LibraryRoots    []string
	DBPath          string
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
	return nil
}

// SplitRoots parses a list of library roots from one environment variable,
// separated the way the platform separates path lists (":" on Unix). A single
// path parses to a single root, so an existing DOCSEARCH_ROOT keeps working.
func SplitRoots(v string) []string {
	var out []string
	for _, p := range filepath.SplitList(v) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
	if len(c.LibraryRoots) == 0 {
		c.LibraryRoots = SplitRoots(os.Getenv(EnvRoot))
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
