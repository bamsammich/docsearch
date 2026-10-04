// Command docsearch is the operator's command line.
//
// Every command but migrate is a client of docsearch-server, which is what
// lets the index sit on another host and what makes one token the only way
// in. migrate is the exception because it writes the schema the server
// expects to find, and so has to run before the server does.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"connectrpc.com/connect"
	_ "github.com/jackc/pgx/v5/stdlib" // the driver a Postgres database is migrated through
	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/schema"
)

func main() {
	if err := command().Run(context.Background(), os.Args); err != nil {
		// A report that already said what was wrong exits on its status
		// alone, so a script can act on it without a second copy of the
		// explanation on stderr.
		var code exitCode
		if errors.As(err, &code) {
			os.Exit(int(code))
		}
		fmt.Fprintln(os.Stderr, "fatal:", plainly(err))
		os.Exit(1)
	}
}

// exitCode is an outcome the command has already explained.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// exit ends a command on a status without printing anything more.
func exit(code int) error { return exitCode(code) }

var (
	errNoTarget = errors.New("name a path or an http(s) URL to read")
	errNoDocID  = errors.New("name the document to act on")
	errNoJobID  = errors.New("name the job to cancel")
)

func command() *cli.Command {
	return &cli.Command{
		Name:  "docsearch",
		Usage: "manage a docsearch index",
		Commands: []*cli.Command{
			addCommand("add"),
			// `ingest` is what add was called before it could take a URL.
			// Kept as a second name rather than a deprecation: it is in the
			// README, in two service units, and in whatever scripts an
			// operator already wrote.
			addCommand("ingest"),
			enqueueCommand(),
			jobsCommand(),
			cancelCommand(),
			listCommand(),
			verifyCommand(),
			inspectCommand(),
			refreshCommand(),
			removeCommand(),
			migrateCommand(),
		},
	}
}

func migrateCommand() *cli.Command {
	return &cli.Command{
		Name:  "migrate",
		Usage: "bring an index up to the schema this build requires",
		Description: "Idempotent, and the only command that writes the schema " +
			"version. Run it as a startup precondition of the worker, which is " +
			"the only writer.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "dsn",
				Usage:   "Postgres `DSN` of the database to migrate",
				Sources: cli.EnvVars(config.EnvDSN),
			},
			&cli.BoolFlag{
				Name:  "check",
				Usage: "report the version, change nothing, and exit 1 if it is not current",
			},
		},
		Action: migrate,
	}
}

func migrate(ctx context.Context, cmd *cli.Command) error {
	dsn := cmd.String("dsn")
	if dsn == "" {
		return fmt.Errorf("no database: pass --dsn or set %s", config.EnvDSN)
	}

	// Nothing to create: the cluster provisions the database, along with the
	// roles and the extension no migration has the privileges to make. Goose
	// runs against whatever is there, and an empty database is the ordinary
	// case rather than a special one.
	db, err := open(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	found, recorded, err := schema.Recorded(ctx, db)
	if err != nil {
		return err
	}
	if cmd.Bool("check") {
		return report(schema.Version, found, recorded)
	}
	return upgrade(ctx, db, schema.Version, found, recorded)
}

// report says where the index stands and refuses where it is not current.
func report(target, found int, recorded bool) error {
	fmt.Printf("database: schema %s, build requires %d\n", at(found, recorded), target)
	if recorded && found == target {
		fmt.Println("up to date")
		return nil
	}
	return errors.New("migration needed: run `docsearch migrate`")
}

// upgrade migrates the index and says what changed, naming every version it
// passed through so an operator can see what they are getting.
func upgrade(ctx context.Context, db *sql.DB, target, found int, recorded bool) error {
	if recorded && found == target {
		fmt.Printf("already at version %d; nothing to do\n", found)
		return nil
	}
	result, err := schema.Migrate(ctx, db)
	if err != nil {
		return err
	}
	fmt.Printf("migrated %s -> %d\n", at(found, recorded), result.To)
	for _, version := range slices.Sorted(maps.Keys(schema.History)) {
		if !recorded || version > found {
			fmt.Printf("  v%d: %s\n", version, schema.History[version])
		}
	}
	return nil
}

// open connects to the index, creating the file only where the caller meant
// to.
// dial opens a database and proves it answers, naming it as called rather
// than as addressed: a DSN can carry a password.
func open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open the database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("open the database: %w", err), db.Close())
	}
	return db, nil
}

// at is a version as an operator reads it.
func at(found int, recorded bool) string {
	if !recorded {
		return "unversioned"
	}
	return fmt.Sprintf("%d", found)
}

// plainly is what the server said, without the wire's vocabulary.
//
// A connect error prints as "not_found: no such document: nope", and the
// operator asking for a document that is not there has no use for the code
// in front of the sentence.
func plainly(err error) string {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return err.Error()
	}
	if connectErr.Code() == connect.CodeUnauthenticated {
		// "401 Unauthorized" on its own sends the reader to the server logs
		// for something they can fix from here.
		return connectErr.Message() + ": set " + config.EnvToken +
			" to the token the server was given"
	}
	return connectErr.Message()
}
