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
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "addr",
				Value:   "127.0.0.1:8765",
				Usage:   "listen `ADDRESS`",
				Sources: cli.EnvVars(config.EnvAddr),
			},
			&cli.StringFlag{
				Name:    "db",
				Usage:   "path to the SQLite database",
				Sources: cli.EnvVars(config.EnvDB),
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
				Name:  "allow-public-bind",
				Usage: "permit binding a non-loopback address",
			},
		},
		Action: run,
	}
}

// origins splits the allowlist, dropping blanks so that a trailing comma
// does not become an origin nothing can match.
func origins(list string) []string {
	var out []string
	for _, origin := range strings.Split(list, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			out = append(out, origin)
		}
	}
	return out
}

func run(_ context.Context, cmd *cli.Command) error {
	cfg := config.Config{
		Addr:         cmd.String("addr"),
		DBPath:       cmd.String("db"),
		LibraryRoots: cmd.StringSlice("root"),
		// The token is read from the environment alone, deliberately not
		// from a flag: an argument is visible to every process on the host
		// through ps.
		BearerToken:     os.Getenv(config.EnvToken),
		AllowedOrigins:  origins(cmd.String("allowed-origins")),
		AllowPublicBind: cmd.Bool("allow-public-bind"),
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
