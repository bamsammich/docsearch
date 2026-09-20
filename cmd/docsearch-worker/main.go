// Command docsearch-worker runs queued ingests.
//
// It shares the database with the MCP server, which reads documents while
// this writes them, so both open it with the same pragmas.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/repository/sqlite"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/worker"
	"github.com/bamsammich/docsearch/internal/source"
	"github.com/bamsammich/docsearch/internal/source/site"
)

func main() {
	if err := command().Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// command describes the worker's interface.
//
// Each flag names the variable it reads, so the environment is documented in
// --help rather than in prose somewhere else. SliceFlagSeparator is what
// keeps DOCSEARCH_ROOT meaning to this binary what it means to the server: a
// list separated the way the platform separates path lists, rather than the
// commas urfave splits on by default.
func command() *cli.Command {
	return &cli.Command{
		Name:  "docsearch-worker",
		Usage: "run queued ingests",
		Description: "Claims one job at a time from ingest_jobs, reads the document or " +
			"site it names, and writes it to the index the server reads.",
		SliceFlagSeparator: string(filepath.ListSeparator),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "db",
				Usage:   "path to the SQLite database",
				Sources: cli.EnvVars(config.EnvDB),
			},
			&cli.StringSliceFlag{
				Name:    "root",
				Usage:   "library `ROOT`; the only paths a job may name. Repeat for more than one.",
				Sources: cli.EnvVars(config.EnvRoot),
			},
			&cli.StringFlag{
				Name:  "fetch-cache",
				Usage: "where a crawl's responses are stored (default: beside the database)",
			},
			&cli.DurationFlag{
				Name:  "lease",
				Value: worker.DefaultLease,
				Usage: "how long a claim holds a job before another worker may take it back",
			},
			&cli.DurationFlag{
				Name:  "poll",
				Value: worker.DefaultPoll,
				Usage: "wait between looks at an empty queue",
			},
			&cli.DurationFlag{
				Name:  "cancel-poll",
				Value: worker.DefaultCancelPoll,
				Usage: "how often a running job is asked whether it has been cancelled",
			},
			&cli.IntFlag{
				Name:  "max-attempts",
				Value: worker.DefaultMaxAttempts,
				Usage: "how many times a job is tried before it is left failed",
			},
			&cli.BoolFlag{
				Name:  "once",
				Usage: "run one job, or exit on an empty queue",
			},
		},
		Action: run,
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	dbPath := cmd.String("db")
	roots := cmd.StringSlice("root")
	if dbPath == "" {
		return fmt.Errorf("no database: pass --db or set %s", config.EnvDB)
	}
	if len(roots) == 0 {
		return fmt.Errorf("no library root: pass --root or set %s", config.EnvRoot)
	}

	cachePath := cmd.String("fetch-cache")
	if cachePath == "" {
		// Raw HTTP responses, kept apart from the search index, beside the
		// database, which is where the operator already grants write access.
		cachePath = filepath.Join(filepath.Dir(dbPath), "fetch-cache.db")
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	db, err := sqlite.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	extractor, err := pdf.New()
	if err != nil {
		return fmt.Errorf("start the PDF engine: %w", err)
	}
	defer func() {
		if err := extractor.Close(); err != nil {
			log.Warn("could not stop the PDF engine", "error", err)
		}
	}()

	service := worker.New(
		sqlite.NewJobs(db),
		ingest.New(sqlite.New(db), time.Now),
		source.New(adapter.New(extractor), roots, cachePath, site.Options{}),
		worker.Options{
			Log:         log,
			Lease:       cmd.Duration("lease"),
			Poll:        cmd.Duration("poll"),
			CancelPoll:  cmd.Duration("cancel-poll"),
			MaxAttempts: cmd.Int("max-attempts"),
			Once:        cmd.Bool("once"),
		},
	)

	// A signal stops the worker between jobs rather than during one: an
	// ingest interrupted part way leaves its lease to expire and is recovered
	// by whoever claims it next, which is worse than letting it finish.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Info("worker started",
		"database", dbPath, "roots", roots, "fetch cache", cachePath)
	if err := service.Run(ctx); err != nil {
		return err
	}
	log.Info("worker stopped")
	return nil
}
