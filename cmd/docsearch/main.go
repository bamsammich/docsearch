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
	"path/filepath"
	"slices"

	"connectrpc.com/connect"
	"github.com/urfave/cli/v3"
	_ "modernc.org/sqlite" // the driver an index is written with

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
				Name:    "db",
				Usage:   "path to the SQLite database",
				Sources: cli.EnvVars(config.EnvDB),
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
	path := cmd.String("db")
	if path == "" {
		return fmt.Errorf("no database: pass --db or set %s", config.EnvDB)
	}

	fresh, err := absent(path)
	if err != nil {
		return err
	}
	checking := cmd.Bool("check")
	if fresh {
		if checking {
			return fmt.Errorf("database does not exist: %s", path)
		}
		return create(ctx, path)
	}

	db, err := open(path, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	found, recorded, err := schema.Recorded(ctx, db)
	if err != nil {
		return err
	}
	if checking {
		return report(path, found, recorded)
	}
	return upgrade(ctx, db, found, recorded)
}

// create writes a new index, which is not a migration: no existing data's
// meaning could be misread.
func create(ctx context.Context, path string) error {
	db, err := open(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := schema.Create(ctx, db); err != nil {
		return err
	}
	fmt.Printf("created %s at version %d\n", path, schema.Version)
	return nil
}

// report says where the index stands and refuses where it is not current.
func report(path string, found int, recorded bool) error {
	fmt.Printf("database %s: schema %s, build requires %d\n",
		path, at(found, recorded), schema.Version)
	if recorded && found == schema.Version {
		fmt.Println("up to date")
		return nil
	}
	return errors.New("migration needed: run `docsearch migrate`")
}

// upgrade migrates the index and says what changed, naming every version it
// passed through so an operator can see what they are getting.
func upgrade(ctx context.Context, db *sql.DB, found int, recorded bool) error {
	if recorded && found == schema.Version {
		fmt.Printf("already at version %d; nothing to do\n", found)
		return nil
	}
	result, err := schema.Migrate(ctx, db)
	if err != nil {
		return err
	}
	fmt.Printf("migrated %s -> %d\n", at(found, recorded), result.To)
	for _, column := range result.ColumnsAdded {
		fmt.Printf("  added column %s\n", column)
	}
	for _, version := range slices.Sorted(maps.Keys(schema.History)) {
		if !recorded || version > found {
			fmt.Printf("  v%d: %s\n", version, schema.History[version])
		}
	}
	return nil
}

// open connects to the index, creating the file only where the caller meant
// to.
func open(path string, fresh bool) (*sql.DB, error) {
	if fresh {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
	}
	db, err := sql.Open("sqlite", path+
		"?_pragma=busy_timeout(5000)"+
		"&_pragma=journal_mode(WAL)"+
		"&_pragma=foreign_keys(ON)"+
		"&_time_format=sqlite")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("open %s: %w", path, err), db.Close())
	}
	return db, nil
}

// absent reports whether the index has yet to be created.
func absent(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return false, nil
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
