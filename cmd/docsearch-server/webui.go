package main

import (
	"strings"

	"github.com/bamsammich/docsearch/internal/authserver"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/webui"
)

// webUI is the library UI's routes, served without a credential.
type webUI struct {
	routes []route
}

// newWebUI serves the embedded app, and tells it how this server signs a
// browser in: the same way it signs in an MCP client.
func newWebUI(cfg config.Config) webUI {
	ui := webui.Config{
		Auth:        cfg.Auth.String(),
		PublicURL:   cfg.PublicURL,
		Origins:     cfg.BrowserOrigins(),
		UploadMaxMB: cfg.UploadMaxMB,
	}
	switch cfg.Auth {
	case owner.OIDC:
		ui.Issuer = cfg.OIDC.Issuer
		ui.ClientID = cfg.UIClientID
		ui.Scopes = cfg.OIDC.RequiredScopes
		ui.Resource = cfg.OIDC.Resource
	case owner.Password:
		// docsearch is its own issuer here. The page registers itself as a
		// public client at /register, so there is no client to configure.
		issuer := strings.TrimSuffix(cfg.PublicURL, "/")
		ui.Issuer = issuer
		ui.Scopes = []string{authserver.Scope}
		ui.Resource = issuer + "/mcp"
	default:
		// none and token need nothing beyond the mode.
	}
	routes := webui.Routes(ui, webui.Built())
	mounted := make([]route, 0, len(routes))
	for _, r := range routes {
		mounted = append(mounted, route{path: r.Path, handler: r.Handler})
	}
	return webUI{routes: mounted}
}
