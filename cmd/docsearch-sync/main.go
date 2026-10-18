// Command docsearch-sync pushes the documents a Paperless-ngx user can see
// into a docsearch library, and retires the ones it no longer can.
//
// It runs where Paperless runs and reaches out to both, so docsearch never
// reaches into the network Paperless lives on. What reaches docsearch is
// decided in Paperless, by what its user is granted.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/api/connectclient"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/docsync"
	"github.com/bamsammich/docsearch/internal/paperless"
	"github.com/bamsammich/docsearch/internal/upload"
)

// Environment the command reads that docsearch's own binaries do not.
const (
	envPaperlessURL   = "PAPERLESS_URL"
	envPaperlessToken = "PAPERLESS_TOKEN" //nolint:gosec // name of a variable, not a credential
)

// requestTimeout bounds one request to either side. A large manual takes a
// while to move, so it is generous.
const requestTimeout = 10 * time.Minute

var (
	errNoPaperless = errors.New(
		"name the Paperless instance: set --paperless-url or " + envPaperlessURL,
	)
	errNoTokens   = errors.New("set both " + envPaperlessToken + " and " + config.EnvToken)
	errSomeFailed = errors.New("some documents did not sync")
)

func main() {
	if err := runMain(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// runMain stops on SIGINT or SIGTERM, finishing nothing it has not started.
func runMain() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return command().Run(ctx, os.Args)
}

func command() *cli.Command {
	return &cli.Command{
		Name:  "docsearch-sync",
		Usage: "push what a Paperless user can see into a docsearch library",
		Description: "Each Paperless document becomes one upload, replaced in place when " +
			"Paperless changes it and removed when Paperless stops showing it. The " +
			"tokens are read from " + envPaperlessToken + " and " + config.EnvToken + " only.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name: "paperless-url", Usage: "`URL` of the Paperless instance",
				Sources: cli.EnvVars(envPaperlessURL),
			},
			&cli.StringFlag{
				Name: "server", Value: "http://127.0.0.1:8765", Usage: "`URL` of docsearch-server",
				Sources: cli.EnvVars(config.EnvServer),
			},
			&cli.StringFlag{
				Name: "key-prefix", Value: "paperless",
				Usage:   "`PREFIX` of every key this sync pushes; one per Paperless instance",
				Sources: cli.EnvVars("DOCSEARCH_SYNC_KEY_PREFIX"),
			},
			&cli.BoolFlag{
				Name:    "prefer-archive",
				Usage:   "push Paperless's archive copy, with its text layer, whenever there is one",
				Sources: cli.EnvVars("DOCSEARCH_SYNC_PREFER_ARCHIVE"),
			},
			&cli.BoolFlag{
				Name:  "allow-empty",
				Usage: "retire everything this sync pushed when Paperless shows nothing",
			},
			&cli.DurationFlag{
				Name:    "every",
				Usage:   "sync again after each `INTERVAL`; 0 syncs once and exits",
				Sources: cli.EnvVars("DOCSEARCH_SYNC_EVERY"),
			},
			&cli.BoolFlag{Name: "dry-run", Usage: "print what would happen, and change nothing"},
		},
		Action: run,
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	syncer, err := build(cmd)
	if err != nil {
		return err
	}
	every, dryRun := cmd.Duration("every"), cmd.Bool("dry-run")
	if every == 0 {
		return pass(ctx, syncer, dryRun)
	}
	return repeat(ctx, syncer, dryRun, every)
}

// repeat syncs every interval until stopped. A failed pass is reported and
// the next one tries again, which is what a long-running sync is for.
func repeat(ctx context.Context, syncer *docsync.Syncer, dryRun bool, every time.Duration) error {
	for {
		if err := pass(ctx, syncer, dryRun); err != nil {
			fmt.Fprintln(os.Stderr, "sync:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
	}
}

func build(cmd *cli.Command) (*docsync.Syncer, error) {
	paperlessURL := cmd.String("paperless-url")
	if paperlessURL == "" {
		return nil, errNoPaperless
	}
	paperlessToken, docsearchToken := os.Getenv(envPaperlessToken), os.Getenv(config.EnvToken)
	if paperlessToken == "" || docsearchToken == "" {
		return nil, errNoTokens
	}
	client := &http.Client{Timeout: requestTimeout}
	server := cmd.String("server")
	return docsync.New(
		paperless.New(client, paperlessURL, paperlessToken),
		connectclient.Default(server, docsearchToken),
		upload.NewClient(client, server, docsearchToken),
		docsync.Options{
			Supports:      adapter.IsSupported,
			Prefix:        cmd.String("key-prefix"),
			PreferArchive: cmd.Bool("prefer-archive"),
			AllowEmpty:    cmd.Bool("allow-empty"),
		},
	), nil
}

// pass is one sync, printed one line per document.
func pass(ctx context.Context, syncer *docsync.Syncer, dryRun bool) error {
	if dryRun {
		actions, err := syncer.Plan(ctx)
		for _, action := range actions {
			fmt.Println(describe(docsync.Outcome{Action: action}))
		}
		return err
	}
	outcomes, err := syncer.Run(ctx)
	if err != nil {
		return err
	}
	failed := 0
	for _, outcome := range outcomes {
		fmt.Println(describe(outcome))
		if outcome.Err != nil {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%w: %d of %d", errSomeFailed, failed, len(outcomes))
	}
	return nil
}

func describe(outcome docsync.Outcome) string {
	line := fmt.Sprintf("%-6s %s", outcome.Kind, outcome.Key)
	switch {
	case outcome.Err != nil:
		return line + ": " + outcome.Err.Error()
	case outcome.Reason != "":
		return line + " (" + outcome.Reason + ")"
	case outcome.Filename != "":
		return line + " " + outcome.Filename
	default:
		return line
	}
}
