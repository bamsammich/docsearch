// Package webui serves the library UI: a single-page app built from web/
// into dist/ and embedded in the server binary.
//
// The app is static and carries no data. Every library read it makes goes
// through the same guarded routes an MCP client uses, with the same bearer,
// so serving it needs no credential and adds no second way in.
package webui

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// built is the app as `npm run build` in web/ leaves it. A checkout that has
// not built it holds only dist/.gitkeep, and the server says so at /.
//
//go:embed all:dist
var built embed.FS

// Built is the embedded app.
func Built() fs.FS {
	files, err := fs.Sub(built, "dist")
	if err != nil {
		// fs.Sub fails only on an invalid path, and "dist" is a literal.
		panic(err)
	}
	return files
}

// Config is what the app learns about the server before anyone signs in.
type Config struct {
	// Auth is the server's auth mode: none, token or oidc.
	Auth string `json:"auth"`
	// Issuer is the provider the browser signs in at, in oidc mode.
	Issuer string `json:"issuer,omitempty"`
	// ClientID is the public client the browser signs in as.
	ClientID string `json:"client_id,omitempty"`
	// Resource is docsearch's /mcp URI, which a token's audience names.
	Resource string `json:"resource,omitempty"`
	// PublicURL is where the server expects a browser to open the UI.
	PublicURL string `json:"public_url,omitempty"`
	// Scopes are requested beside openid.
	Scopes []string `json:"scopes,omitempty"`
	// Origins are the browser origins the server accepts requests from. Not
	// sent: the page is told only whether its own address is among them.
	Origins     []string `json:"-"`
	UploadMaxMB int64    `json:"upload_max_mb"`
}

// answer is Config as one page reads it: whether the address it was opened
// at is one the server accepts, so a refusal can be named before it happens.
type answer struct {
	Config

	OriginAllowed bool `json:"origin_allowed"`
}

// Route is one path the UI answers on.
type Route struct {
	Handler http.Handler
	Path    string
}

// ConfigPath is where the app reads Config.
const ConfigPath = "/ui/config"

// Routes are the UI's paths: the page, its hashed assets, and Config.
func Routes(cfg Config, files fs.FS) []Route {
	page := pageHandler(files, contentSecurityPolicy(cfg.Issuer))
	assets := http.FileServerFS(files)
	return []Route{
		{Path: "GET /{$}", Handler: page},
		{Path: "GET /favicon.svg", Handler: assets},
		{Path: "GET /assets/", Handler: immutable(assets)},
		{Path: "GET " + ConfigPath, Handler: configHandler(cfg)},
	}
}

// notBuilt answers / in a binary built before the app was.
const notBuilt = `<!doctype html><meta charset="utf-8"><title>docsearch</title>
<p>The library UI was not built into this binary. Run <code>mise run web</code>, then build the server again.</p>
`

func pageHandler(files fs.FS, policy string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, err := fs.ReadFile(files, "index.html")
		header := w.Header()
		header.Set("Content-Type", "text/html; charset=utf-8")
		header.Set("Cache-Control", "no-cache")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, notBuilt)
			return
		}
		header.Set("Content-Security-Policy", policy)
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(page))
	})
}

// immutable marks the hashed build output cacheable for good: a new build
// names its files anew, and index.html, which names them, is never cached.
func immutable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func configHandler(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := json.Marshal(
			answer{Config: cfg, OriginAllowed: accepts(cfg.Origins, pageOrigin(r))},
		)
		if err != nil {
			http.Error(w, "could not describe the server", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "config.json", time.Time{}, bytes.NewReader(body))
	})
}

// pageOrigin is the origin the page asking was loaded from: a same-origin
// GET carries no Origin header, so it is rebuilt from the address the browser
// asked for. It is a hint for the page, never a check; RequireAllowedOrigin
// is the check.
func pageOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func accepts(origins []string, origin string) bool {
	for _, o := range origins {
		if strings.EqualFold(strings.TrimSpace(o), origin) {
			return true
		}
	}
	return false
}

// contentSecurityPolicy lets the page load only its own files, and reach
// only this server and, in oidc mode, the provider it signs in at.
func contentSecurityPolicy(issuer string) string {
	connect := "'self'"
	if origin := originOf(issuer); origin != "" {
		connect += " " + origin
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src " + connect,
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
	}, "; ")
}

// originOf is scheme://host of an absolute URL, or empty.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
