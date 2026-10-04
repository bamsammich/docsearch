// Package schema creates a docsearch index and brings an existing one up to
// date.
//
// Migrations run through goose, which numbers and orders them and records
// what it applied. Version 5 is the floor: the baseline is the schema
// docsearch started from, and every later change is its own numbered file.
// Versions 1 to 4 are history rather than migrations, since the DDL that
// produced them was never kept, and the first four happened on SQLite, which
// phase 04 replaced.
//
// Nothing here creates a database, its roles or pg_textsearch. A migration
// runs as a role that owns the schema and nothing more, which is what
// CloudNativePG's app user is, so a migration reaching for a superuser
// privilege would fail in the one place it matters. What a cluster owes
// docsearch is stated in docs/plans/postgres-multiuser.md.
//
// Opening a database does not migrate it. Opening used to imply migrating,
// and because every read path opens the database, list, verify and jobs all
// rewrote the recorded version: running an older build against a newer index
// stamped it backwards, and the newer server then refused to serve an index
// that was perfectly sound. Migrating is something an operator asks for.
package schema

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/pressly/goose/v3"
)

// migrations are the numbered migrations, carried inside the binary so a
// deployment is one file.
//
//go:embed migrations/postgres/*.sql
var migrations embed.FS

// Version is bumped whenever the schema changes in a way a reader must know
// about.
//
// Column presence is not sufficient. Adding a column is visible by
// inspection, but changing what an existing column means, as index_terms.
// section did when it began holding section numbers rather than page
// numbers, is invisible to any structural check while silently changing what
// queries return.
const Version = 9

// History is readable, so a version mismatch can be diagnosed without
// reading the git log.
var History = map[int]string{
	1: "initial: documents, ingest_jobs, chunks + FTS5, pages, index_terms",
	2: "index_terms.section replaces page; chunks gains section, printed_page_start, image_count",
	3: "documents.warnings and ingest_jobs.warnings (JSON StructureReport); " +
		"ingest_jobs.permanent distinguishes deterministic failure from exhaustion",
	4: "chunks.kind marks self-declared keyword-reference families so search " +
		"can deprioritise them without deleting them",
	5: "documents.source_kind names what a source is; chunks.url and chunks.fragment " +
		"carry the address a chunk was read from, so a result can be cited",
	6: "every row belongs to a user: user_id on each tenant table, chunks " +
		"partitioned by owner, and row-level security on all five",
	7: "chunks are searchable: one BM25 index over the heading twice then the body",
	8: "the readiness probe may read schema_version, which version 6 left ungranted",
	9: "the crawl's response cache moves into the database: responses and robots, " +
		"per user like every other tenant table",
}

// The tables this package names. Written once, so a typo cannot reach a
// PRAGMA that would then report a column missing from a table that does not
// exist.
const (
	tableUsers      = "users"
	tableDocuments  = "documents"
	tableJobs       = "ingest_jobs"
	tableChunks     = "chunks"
	tablePages      = "pages"
	tableIndexTerms = "index_terms"
	tableResponses  = "responses"
	tableRobots     = "robots"
)

// RequiredTables are what the readiness gate and the CLI check for to decide
// the schema is present.
//
// Searching needs no table of its own: the BM25 index version 7 declares is
// an index on chunks, which is not something a check can look for. The crawl
// cache is here although it is worker-only and holds nothing a search reads,
// because a database missing it is one a migration left half applied, and
// the probe exists to say so.
func RequiredTables() []string {
	return []string{
		tableUsers,
		tableDocuments,
		tableJobs,
		tableChunks,
		tablePages,
		tableIndexTerms,
		tableResponses,
		tableRobots,
	}
}

// ErrTooNew reports an index written by a newer build. Nothing here can know
// what changed, so serving it would be a guess.
type ErrTooNew struct{ Found, Supported int }

func (e *ErrTooNew) Error() string {
	return fmt.Sprintf(
		"database is at version %d, newer than the version %d this build supports. "+
			"Upgrade docsearch.", e.Found, e.Supported)
}

// ErrOutdated reports an index an older build wrote.
type ErrOutdated struct {
	Found     int
	Supported int
	// Recorded is false where no version was ever written.
	Recorded bool
}

func (e *ErrOutdated) Error() string {
	at := fmt.Sprintf("version %d", e.Found)
	if !e.Recorded {
		at = "unversioned"
	}
	return fmt.Sprintf(
		"database is at %s, this build requires version %d. "+
			"Run `docsearch migrate` to upgrade it.", at, e.Supported)
}

// Result is what one migration did.
type Result struct {
	From int
	To   int
	// FromRecorded is false where the database carried no version.
	FromRecorded bool
}

// Create writes the schema into an empty database and records the version.
//
// Initialising an empty file is not a migration: no existing data's meaning
// could be misread, so there is nothing to check first. It runs the same
// migrations all the same, so a fresh index and a migrated one are the same
// index.
func Create(ctx context.Context, db *sql.DB) error {
	_, err := Migrate(ctx, db)
	return err
}

// Migrate brings a database up to Version. It is idempotent, and the only
// thing in the system that writes the version.
func Migrate(ctx context.Context, db *sql.DB) (*Result, error) {
	if err := RequireExtensions(ctx, db); err != nil {
		return nil, err
	}
	before, recorded, err := Recorded(ctx, db)
	if err != nil {
		return nil, err
	}
	if recorded && before > Version {
		return nil, &ErrTooNew{Found: before, Supported: Version}
	}
	if err := up(ctx, db); err != nil {
		return nil, err
	}
	if err := verify(ctx, db, Version); err != nil {
		return nil, err
	}
	if err := stamp(ctx, db, Version); err != nil {
		return nil, err
	}
	return &Result{From: before, To: Version, FromRecorded: recorded}, nil
}

// verify refuses to let a stamp claim what the schema does not hold.
func verify(ctx context.Context, db *sql.DB, target int) error {
	problems, err := Problems(ctx, db)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf(
			"migrated to version %d, but the schema is not what that version means: %s",
			target, strings.Join(problems, " "))
	}
	return nil
}

// up applies every migration the database has yet to see.
func up(ctx context.Context, db *sql.DB) error {
	provider, err := Provider(db)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Provider is goose over the embedded migrations.
//
// Exported for the migration tests, which roll each migration back and apply
// it again against a real database. Nothing in the running system rolls a
// migration back: an index is regenerable, so a bad migration is answered by
// rebuilding rather than by undoing.
func Provider(db *sql.DB) (*goose.Provider, error) {
	// goose reads from the root of the filesystem it is given, and the
	// migrations are embedded under a directory of their own.
	rooted, err := fs.Sub(migrations, "migrations/postgres")
	if err != nil {
		return nil, fmt.Errorf("read the migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, rooted)
	if err != nil {
		return nil, fmt.Errorf("read the migrations: %w", err)
	}
	return provider, nil
}

// Extensions are what a cluster installs before docsearch connects.
// Creating one needs a superuser, which docsearch is not, so the only thing
// it can usefully do is say which one is absent.
var Extensions = []string{"pg_textsearch"}

// ErrMissingExtension names an extension the database does not have.
type ErrMissingExtension struct{ Name string }

func (e *ErrMissingExtension) Error() string {
	return fmt.Sprintf(
		"the %s extension is not installed in this database. Creating it requires a "+
			"superuser, so the cluster installs it: add %s to the cluster's "+
			"shared_preload_libraries and create it in this database.", e.Name, e.Name)
}

// RequireExtensions reports the first extension the database lacks.
//
// Checked before a migration rather than at the first query that needs one,
// because a failure here names what to do and a failure there names an
// operator that does not exist.
func RequireExtensions(ctx context.Context, db *sql.DB) error {
	for _, name := range Extensions {
		var present bool
		err := db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = $1)`, name).Scan(&present)
		if err != nil {
			return fmt.Errorf("look for the %s extension: %w", name, err)
		}
		if !present {
			return &ErrMissingExtension{Name: name}
		}
	}
	return nil
}

// Check reports whether a database is at the version this build requires,
// without changing it.
func Check(ctx context.Context, db *sql.DB) error {
	found, recorded, err := Recorded(ctx, db)
	if err != nil {
		return err
	}
	switch {
	case recorded && found > Version:
		return &ErrTooNew{Found: found, Supported: Version}
	case !recorded || found != Version:
		return &ErrOutdated{Found: found, Supported: Version, Recorded: recorded}
	}
	return nil
}

// Recorded is the version the database carries, and whether it carries one.
func Recorded(ctx context.Context, db *sql.DB) (int, bool, error) {
	var version int
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_version LIMIT 1`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		// A database with no schema_version table predates the stamp, which
		// is the same answer as an empty one: nothing was recorded.
		return 0, false, nil
	}
	return version, true, nil
}

// Problems is what must hold before a version may be recorded.
//
// A stamp is a claim that the database matches the code. Writing it without
// checking makes the claim unfalsifiable: the readiness gate then passes on a
// database that only says it migrated, and the failure surfaces later as a
// query error rather than at the gate.
func Problems(ctx context.Context, db *sql.DB) ([]string, error) {
	present, err := tables(ctx, db)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, table := range RequiredTables() {
		if !present[table] {
			problems = append(problems, "table "+table+" is missing")
		}
	}
	return problems, nil
}

// stamp records the version in schema_version.
//
// goose keeps its own record of which migrations ran. This one answers a
// different question, which a reader asks before it trusts the shape: what
// version does this database claim to be.
func stamp(ctx context.Context, db *sql.DB, version int) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
		return fmt.Errorf("clear the recorded version: %w", err)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO schema_version (version, applied_at) VALUES ($1, now())`, version)
	if err != nil {
		return fmt.Errorf("record version %d: %w", version, err)
	}
	return nil
}

// tables is every table and view the database holds.
func tables(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT table_name FROM information_schema.tables
		  WHERE table_schema = current_schema()`)
	if err != nil {
		return nil, fmt.Errorf("read the table list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read a table name: %w", err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the table list: %w", err)
	}
	return present, nil
}
