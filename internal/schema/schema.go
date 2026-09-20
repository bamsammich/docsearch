// Package schema creates a docsearch index and brings an existing one up to
// date.
//
// Migrations run through goose, which numbers and orders them, records what
// it applied, and does the same for Postgres in phase 04. Version 5 is the
// floor: the baseline migration is the schema as it stands, generated from
// python/docsearch/schema.sql, and every later change is its own numbered
// file. Versions 1 to 4 are history rather than migrations, since the DDL
// that produced them was never kept; an index still at one of them is
// repaired by the column backfill this package runs after goose.
//
// The baseline is idempotent, and has to stay that way while indexes exist
// that goose has never seen: the Python pipeline stamps schema_version and
// writes no goose record, so goose meeting one of those applies the baseline
// over a schema that is already there.
//
// Opening a database does not migrate it. Opening used to imply migrating,
// and because every read path opens the database, list, verify and jobs all
// rewrote the recorded version: running an older build against a newer index
// stamped it backwards, and the newer server then refused to serve an index
// that was perfectly sound. Migrating is something an operator asks for.
//
// Ported from python/docsearch/db.py.
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
//go:embed migrations/*.sql
var migrations embed.FS

// Version is bumped whenever the schema changes in a way a reader must know
// about.
//
// Column presence is not sufficient. Adding a column is visible by
// inspection, but changing what an existing column means, as index_terms.
// section did when it began holding section numbers rather than page
// numbers, is invisible to any structural check while silently changing what
// queries return.
const Version = 5

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
}

// The tables this package names. Written once, so a typo cannot reach a
// PRAGMA that would then report a column missing from a table that does not
// exist.
const (
	tableDocuments  = "documents"
	tableJobs       = "ingest_jobs"
	tableChunks     = "chunks"
	tableChunksFTS  = "chunks_fts"
	tablePages      = "pages"
	tableIndexTerms = "index_terms"
)

// RequiredTables are what the readiness gate and the CLI check for to decide
// the schema is present.
var RequiredTables = []string{
	tableDocuments,
	tableJobs,
	tableChunks,
	tableChunksFTS,
	tablePages,
	tableIndexTerms,
}

// addedColumns are the columns added after the initial schema. CREATE TABLE
// IF NOT EXISTS leaves an existing table untouched, so a new column needs an
// explicit backfill.
var addedColumns = []struct {
	table  string
	column string
	decl   string
}{
	{tableDocuments, "warnings", text},
	{tableJobs, "warnings", text},
	{tableJobs, "permanent", "INTEGER NOT NULL DEFAULT 0"},
	{tableChunks, "kind", "TEXT NOT NULL DEFAULT 'prose'"},
	{tableDocuments, "source_kind", "TEXT NOT NULL DEFAULT 'file'"},
	{tableChunks, "url", text},
	{tableChunks, "fragment", text},
}

// text is the declaration a nullable text column takes.
const text = "TEXT"

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

// ErrUnmigratable reports a database the migration will not stamp, and what
// stopped it.
type ErrUnmigratable struct {
	Problems []string
	Found    int
	Target   int
	Recorded bool
}

func (e *ErrUnmigratable) Error() string {
	at := fmt.Sprintf("%d", e.Found)
	if !e.Recorded {
		at = "unversioned"
	}
	return fmt.Sprintf(
		"cannot migrate from %s to version %d; the version was NOT recorded. %s",
		at, e.Target, strings.Join(e.Problems, " "))
}

// Result is what one migration did.
type Result struct {
	ColumnsAdded []string
	From         int
	To           int
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

	// The backfill repairs an index from before version 5, whose migrations
	// were never written down. CREATE TABLE IF NOT EXISTS leaves an existing
	// table untouched, so the baseline alone would not add a column to one.
	added, err := backfill(ctx, db)
	if err != nil {
		return nil, err
	}

	problems, err := Problems(ctx, db)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, &ErrUnmigratable{
			Problems: problems, Found: before, Target: Version, Recorded: recorded,
		}
	}
	if err := stamp(ctx, db, Version); err != nil {
		return nil, err
	}
	return &Result{
		ColumnsAdded: added, From: before, To: Version, FromRecorded: recorded,
	}, nil
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
	rooted, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read the migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, rooted)
	if err != nil {
		return nil, fmt.Errorf("read the migrations: %w", err)
	}
	return provider, nil
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

// backfill adds the columns an older schema lacks, and names what it added.
func backfill(ctx context.Context, db *sql.DB) ([]string, error) {
	added := []string{}
	for _, c := range addedColumns {
		has, err := hasColumn(ctx, db, c.table, c.column)
		if err != nil {
			return nil, err
		}
		if has {
			continue
		}
		statement := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.column, c.decl)
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return nil, fmt.Errorf("add %s.%s: %w", c.table, c.column, err)
		}
		added = append(added, c.table+"."+c.column)
	}
	return added, nil
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
	for _, table := range RequiredTables {
		if !present[table] {
			problems = append(problems, "table "+table+" is missing")
		}
	}
	missing, err := missingColumns(ctx, db, present)
	if err != nil {
		return nil, err
	}
	problems = append(problems, missing...)

	stale, err := predatesSectionReferences(ctx, db, present)
	if err != nil {
		return nil, err
	}
	if stale != "" {
		problems = append(problems, stale)
	}
	return problems, nil
}

// missingColumns names every column an older schema left behind.
func missingColumns(
	ctx context.Context,
	db *sql.DB,
	present map[string]bool,
) ([]string, error) {
	var missing []string
	for _, c := range addedColumns {
		if !present[c.table] {
			continue
		}
		has, err := hasColumn(ctx, db, c.table, c.column)
		if err != nil {
			return nil, err
		}
		if !has {
			missing = append(missing, fmt.Sprintf("column %s.%s is missing", c.table, c.column))
		}
	}
	return missing, nil
}

// predatesSectionReferences reports why an index from before version 2
// cannot be migrated, or "" where it is not one.
//
// The change was semantic rather than additive: index_terms.section held
// printed page numbers and now holds section numbers. Nothing recovers one
// from the other without the source document, so it cannot be migrated in
// place, and the index is regenerable, so it need not be. Refusing beats
// stamping a version the data does not match.
func predatesSectionReferences(
	ctx context.Context,
	db *sql.DB,
	present map[string]bool,
) (string, error) {
	if !present[tableIndexTerms] {
		return "", nil
	}
	sections, err := hasColumn(ctx, db, tableIndexTerms, "section")
	if err != nil || sections {
		return "", err
	}
	return "index_terms has no 'section' column, so this database predates the change " +
		"from page references to section references. No transformation recovers " +
		"section numbers from page numbers without the source documents. The index " +
		"is fully regenerable: delete it and re-ingest the library.", nil
}

// stamp records the version in schema_version.
//
// goose keeps its own record, which is what Go reads. This second one is
// what the Python pipeline reads, and it goes when Python does.
func stamp(ctx context.Context, db *sql.DB, version int) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
		return fmt.Errorf("clear the recorded version: %w", err)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO schema_version (version, applied_at) VALUES (?, datetime('now'))`,
		version)
	if err != nil {
		return fmt.Errorf("record version %d: %w", version, err)
	}
	return nil
}

// tables is every table and view the database holds.
func tables(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type IN ('table','view')`)
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

func hasColumn(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	// PRAGMA table_info takes no placeholder, and the table names come from
	// this package's own tables rather than from a caller.
	rows, err := db.QueryContext(
		ctx,
		fmt.Sprintf("PRAGMA table_info(%s)", table),
	) //nolint:gosec // table names are this package's constants
	if err != nil {
		return false, fmt.Errorf("read the columns of %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType sql.NullString
			notNull    int
			dflt       sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &dflt, &primaryKey); err != nil {
			return false, fmt.Errorf("read a column of %s: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read the columns of %s: %w", table, err)
	}
	return false, nil
}
