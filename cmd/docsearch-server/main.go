// Command docsearch-server serves two APIs over one service layer.
//
// Claude speaks MCP and cannot be asked to speak anything else, so the
// document tools stay on /mcp. Every other client gets the typed ConnectRPC
// API: the docsearch CLI, docsearch-sync beside Paperless, and any later web
// UI through Connect-Web.
//
// Both APIs check the same token, a Connect interceptor on one side and the
// bearer middleware on the other, because a second way in with its own idea
// of who may use it is how one of them ends up wrong.
//
// The command line is described here; everything the server is built from is
// wired in wire.go.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
)

func main() {
	if err := command().Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// command describes the server's interface.
//
// Each flag names the variable it reads, so the environment is documented in
// --help rather than in prose somewhere else, and urfave does the reading.
// SliceFlagSeparator keeps DOCSEARCH_ROOT meaning to this binary what it
// means to docsearch-worker: a list separated the way the platform separates
// path lists, rather than the commas urfave splits on by default.
func command() *cli.Command {
	return &cli.Command{
		Name:  "docsearch-server",
		Usage: "serve the document tools over MCP and ConnectRPC",
		Description: "Serves /mcp for Claude and a typed ConnectRPC API for every other " +
			"client, from one index and behind one bearer token.",
		SliceFlagSeparator: string(filepath.ListSeparator),
		Flags: append([]cli.Flag{
			&cli.StringFlag{
				Name:    "addr",
				Value:   "127.0.0.1:8765",
				Usage:   "listen `ADDRESS`",
				Sources: cli.EnvVars(config.EnvAddr),
			},
			&cli.StringFlag{
				Name:    "dsn",
				Usage:   "Postgres `DSN` of the database holding every library",
				Sources: cli.EnvVars(config.EnvDSN),
			},
			&cli.StringFlag{
				Name:    "auth",
				Usage:   "how a request's user is decided: one of " + modes(),
				Value:   owner.Token.String(),
				Sources: cli.EnvVars(config.EnvAuth),
			},
			&cli.StringSliceFlag{
				Name:    "root",
				Usage:   "library `ROOT`; the only paths add_document accepts. Repeat for more than one.",
				Sources: cli.EnvVars(config.EnvRoot),
			},
			// Origins are comma-separated rather than a slice flag: urfave
			// takes one separator per command, roots need the path-list
			// separator, and an origin carries a colon of its own in
			// http://localhost:3000.
			&cli.StringFlag{
				Name:    "allowed-origins",
				Usage:   "comma-separated browser `ORIGIN` allowlist",
				Sources: cli.EnvVars(config.EnvOrigins),
			},
			&cli.BoolFlag{
				Name:    "allow-public-bind",
				Usage:   "permit binding a non-loopback address",
				Sources: cli.EnvVars(config.EnvAllowPublicBind),
			},
		}, oidcFlags()...),
		Action: run,
	}
}

// oidcFlags are the settings auth mode oidc reads, and nothing else does.
//
// Scopes are comma-separated strings rather than slice flags for the reason
// origins are: a scope often carries a colon, which is the separator slice
// flags split on here.
func oidcFlags() []cli.Flag {
	text := func(name, env, usage string) cli.Flag {
		return &cli.StringFlag{Name: name, Usage: usage, Sources: cli.EnvVars(env)}
	}
	return []cli.Flag{
		text("oidc-issuer", config.EnvOIDCIssuer, "the provider's issuer `URL`"),
		text("oidc-resource", config.EnvOIDCResource,
			"docsearch's own /mcp `URI`, as clients address it"),
		text("oidc-audience", config.EnvOIDCAudience,
			"the aud `VALUE` a token must carry, when the provider binds its own"),
		text("oidc-scopes-supported", config.EnvOIDCScopesSupported,
			"comma-separated `SCOPES` a client should request"),
		text("oidc-required-scopes", config.EnvOIDCRequiredScopes,
			"comma-separated `SCOPES` every token must carry"),
		text("oidc-client-id", config.EnvOIDCClientID,
			"docsearch's client `ID` at the provider, to introspect opaque tokens"),
		text("oidc-default-owner", config.EnvOIDCDefaultOwner,
			"the `SUBJECT` who inherits the library made before accounts existed"),
		text("oidc-required-claim-name", config.EnvOIDCRequiredClaimName,
			"a `CLAIM` every token must carry, such as groups"),
		text("oidc-required-claim-value", config.EnvOIDCRequiredClaimValue,
			"the `VALUE` the required claim must hold"),
		&cli.BoolFlag{
			Name:    "oidc-sign-up",
			Usage:   "create an account for an identity that has none",
			Sources: cli.EnvVars(config.EnvOIDCSignUp),
		},
	}
}

// commaList splits a comma-separated setting, dropping blanks so that a
// trailing comma does not become an entry nothing can match.
func commaList(list string) []string {
	var out []string
	for _, origin := range strings.Split(list, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			out = append(out, origin)
		}
	}
	return out
}

// modes lists the spellings the auth flag accepts, for its own help.
func modes() string { return strings.Join(owner.Spellings(), ", ") }

func run(_ context.Context, cmd *cli.Command) error {
	auth, err := owner.ParseMode(cmd.String("auth"))
	if err != nil {
		return err
	}
	cfg := config.Config{
		Addr:         cmd.String("addr"),
		DSN:          cmd.String("dsn"),
		Auth:         auth,
		LibraryRoots: cmd.StringSlice("root"),
		// The token is read from the environment alone, deliberately not
		// from a flag: an argument is visible to every process on the host
		// through ps.
		BearerToken:     os.Getenv(config.EnvToken),
		AllowedOrigins:  commaList(cmd.String("allowed-origins")),
		AllowPublicBind: cmd.Bool("allow-public-bind"),
		OIDC: config.OIDC{
			Issuer:             cmd.String("oidc-issuer"),
			Resource:           cmd.String("oidc-resource"),
			Audience:           cmd.String("oidc-audience"),
			ClientID:           cmd.String("oidc-client-id"),
			ClientSecret:       os.Getenv(config.EnvOIDCClientSecret),
			DefaultOwner:       cmd.String("oidc-default-owner"),
			RequiredClaimName:  cmd.String("oidc-required-claim-name"),
			RequiredClaimValue: cmd.String("oidc-required-claim-value"),
			ScopesSupported:    commaList(cmd.String("oidc-scopes-supported")),
			RequiredScopes:     commaList(cmd.String("oidc-required-scopes")),
			SignUp:             cmd.Bool("oidc-sign-up"),
		},
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := cfg.Validate(); err != nil {
		return err
	}
	if !config.IsLoopbackAddr(cfg.Addr) {
		log.Warn("binding a non-loopback address",
			"addr", cfg.Addr,
			"warning", "this service exposes a filesystem path parameter; it must sit behind "+
				"a trusted network boundary such as Tailscale, never a public listener")
	}

	return serve(cfg, log)
}
