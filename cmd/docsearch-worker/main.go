// Command docsearch-worker runs queued ingests.
//
// It shares the database with the server, which reads documents while this
// writes them. A worker serves one queue at a time, the jobs one person
// queued in one library, and writes as that person.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the driver the library is read through
	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/worker"
	"github.com/bamsammich/docsearch/internal/site/fetch/pgcache"
	"github.com/bamsammich/docsearch/internal/source"
	"github.com/bamsammich/docsearch/internal/source/site"
	"github.com/bamsammich/docsearch/internal/upload/pgupload"
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
				Name:    "dsn",
				Usage:   "Postgres `DSN` of the database holding every library",
				Sources: cli.EnvVars(config.EnvDSN),
			},
			&cli.StringFlag{
				Name: "user",
				Usage: "serve only the jobs this `USER` queued in their own library. " +
					"Unset, the worker takes a job from every waiting queue in turn, so " +
					"a new account or library is served from its first upload.",
				Sources: cli.EnvVars(config.EnvUser),
			},
			&cli.StringSliceFlag{
				Name:    "root",
				Usage:   "library `ROOT`; the only paths a job may name. Repeat for more than one.",
				Sources: cli.EnvVars(config.EnvRoot),
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
				Name: "once",
				Usage: "with --user, run one job or exit on an empty queue; without it, " +
					"run until every queue is empty",
			},
		},
		Action: run,
	}
}

// required names whichever setting the operator left out, rather than
// failing later on a connection string nobody passed.
func required(dsn string, roots []string) error {
	switch {
	case dsn == "":
		return fmt.Errorf("no database: pass --dsn or set %s", config.EnvDSN)
	case len(roots) == 0:
		return fmt.Errorf("no library root: pass --root or set %s", config.EnvRoot)
	}
	return nil
}

func run(ctx context.Context, cmd *cli.Command) error {
	dsn := cmd.String("dsn")
	roots := cmd.StringSlice("root")
	user := cmd.String("user")
	if err := required(dsn, roots); err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	pgsession.Bound(db, pgsession.WorkerConnections)
	if err := db.Ping(); err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	// One job runs at a time, so one document is ever open.
	extractor, err := pdf.New(1)
	if err != nil {
		return fmt.Errorf("start the PDF engine: %w", err)
	}
	defer func() {
		if err := extractor.Close(); err != nil {
			log.Warn("could not stop the PDF engine", "error", err)
		}
	}()

	options := worker.Options{
		Log:         log,
		Lease:       cmd.Duration("lease"),
		Poll:        cmd.Duration("poll"),
		CancelPoll:  cmd.Duration("cancel-poll"),
		MaxAttempts: cmd.Int("max-attempts"),
		Once:        cmd.Bool("once"),
	}
	// Every one of the three reads and writes in the same scope, because a
	// job, the document it produces and the responses it crawled all belong
	// to one library, and are written as the person who queued the job.
	library := func(queue worker.Queue) *worker.Service {
		scope := pgsession.Scope{User: queue.User, Library: queue.Library}
		return worker.New(
			postgres.NewJobs(db, scope),
			ingest.New(postgres.New(db, scope), time.Now),
			source.New(adapter.New(extractor), roots, pgcache.New(db, scope),
				pgupload.New(db, scope), site.Options{}),
			options,
		)
	}

	// A signal stops the worker between jobs rather than during one: an
	// ingest interrupted part way leaves its lease to expire and is recovered
	// by whoever claims it next, which is worse than letting it finish.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := serve(ctx, user, library, db, options, log.With("roots", roots)); err != nil {
		return err
	}
	log.Info("worker stopped")
	return nil
}

// serve runs one queue's worker when a user is pinned, and otherwise a
// rotation over every waiting queue.
func serve(
	ctx context.Context,
	user string,
	library func(worker.Queue) *worker.Service,
	db *sql.DB,
	options worker.Options,
	log *slog.Logger,
) error {
	if user != "" {
		log.Info("worker started", "user", user)
		return library(worker.Queue{User: user, Library: user}).Run(ctx)
	}
	log.Info("worker started", "user", "every waiting queue")
	return worker.NewRotation(postgres.NewQueues(db), library, options.Poll, options.Once, log).
		Run(ctx)
}
