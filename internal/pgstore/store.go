// Package pgstore is the server's data access layer on Postgres.
//
// Every read path filters documents.status='ready'. A document is written
// incrementally and must not appear in any result until its ingest
// completes.
//
// Row-level security decides which library a statement sees, so nothing here
// touches the database outside a transaction that named its user:
// internal/pgsession is the only way in.
package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // the driver the server and worker use

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/store/pgdbgen"
)

// ErrNotFound is returned when a requested document or job does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the shared SQLite database.
//
// q holds the queries generated from the migrations. The two
// statements it cannot express -- Search, which composes its WHERE clause from
// the request and calls bm25(), and matchingIndexSections, which builds one
// term per query word -- run through db directly.
type Store struct {
	db *sql.DB
	q  *pgdbgen.Queries
	// userID is the library every statement reads. Bound once, because a
	// store serves one request and a request belongs to one user; step 4f
	// hands it down from the service layer.
	userID string
}

// New reads one user's library through db, which the caller opens and
// closes.
func New(db *sql.DB, userID string) *Store {
	return &Store{db: db, q: pgdbgen.New(db), userID: userID}
}

// Open connects to a Postgres database.
//
// No pragmas to agree on: what SQLite needed busy_timeout and WAL for, two
// processes reading and writing at once, Postgres does by default.
func Open(dsn, userID string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open the index: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("open the index: %w", err), db.Close())
	}
	return New(db, userID), nil
}

func (s *Store) Close() error { return s.db.Close() }

// The statuses and the grade a caller sees, written once.
const (
	statusQueued   = "queued"
	statusRunning  = "running"
	qualityUnknown = "unknown"
)

// timeFormat is how a timestamp reads when a tool prints one.
const timeFormat = "2006-01-02T15:04:05Z"

// nullInt converts a nullable integer to the *int the tool schemas
// use, where absent means "not applicable to this document" rather than zero.
func nullInt(v sql.NullInt32) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int32)
	return &n
}

// RequiredSchemaVersion is the schema this package was built against.
//
// A version is checked rather than a set of columns because the two catch
// different faults. Column presence catches an *added* column. It cannot catch
// changed semantics on an existing one -- index_terms.section holding section
// numbers where it once held page numbers passes every structural check while
// silently changing what the index-term boost resolves to. Only a version
// number, bumped deliberately, catches that.
const RequiredSchemaVersion = schema.Version

// ErrSchemaVersion reports a database written by a different schema revision.
type ErrSchemaVersion struct {
	Found    int
	Required int
}

func (e *ErrSchemaVersion) Error() string {
	found := fmt.Sprintf("%d", e.Found)
	if e.Found == 0 {
		found = "unversioned (predates schema versioning)"
	}
	return fmt.Sprintf(
		"schema version mismatch: database is at version %s, this server requires "+
			"version %d. Run the ingester against this database to migrate it, or "+
			"deploy the server build matching the database.", found, e.Required)
}

// Ready reports whether the database is openable and at the expected schema
// version. It must not disclose document titles, paths or counts -- it is
// reachable without a token.
func (s *Store) Ready(ctx context.Context) error {
	// The table list comes from internal/schema, so the gate and the
	// migration cannot disagree about what a complete schema is. Neither
	// table it reads carries a policy, which is why this needs no user: the
	// probe answers before anyone has signed in.
	for _, name := range schema.RequiredTables(schema.Postgres) {
		var found string
		err := s.db.QueryRowContext(ctx,
			`SELECT table_name FROM information_schema.tables
			  WHERE table_schema = current_schema() AND table_name = $1`,
			name).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("schema incomplete: %s is missing", name)
		}
		if err != nil {
			return errors.New("schema check failed")
		}
	}

	version, err := s.q.SchemaVersion(ctx)
	if err != nil {
		// Covers both no row and no schema_version table at all: either way
		// this is a database from before versioning.
		return &ErrSchemaVersion{Found: 0, Required: RequiredSchemaVersion}
	}
	if int(version) != RequiredSchemaVersion {
		return &ErrSchemaVersion{Found: int(version), Required: RequiredSchemaVersion}
	}
	return nil
}

// -- documents ------------------------------------------------------------

// Document is a ready document as reported by list_documents.
type Document struct {
	DocID string `json:"doc_id"`
	Title string `json:"title"`
	// Format is the extraction format; SourceKind is where it came from.
	// A site is 'site' whatever its pages were parsed as.
	Format     string   `json:"format"`
	SourceKind string   `json:"source_kind"`
	PageCount  *int     `json:"page_count,omitempty"`
	ChunkCount *int     `json:"chunk_count,omitempty"`
	Quality    string   `json:"quality"`
	Warnings   []string `json:"warnings,omitempty"`
	TopHeaders []string `json:"top_level_headings,omitempty"`
}

const maxTopHeadings = 15

// ListDocuments returns every ready document with its top-level headings.
func (s *Store) ListDocuments(ctx context.Context) ([]Document, error) {
	rows, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) ([]pgdbgen.ListReadyDocumentsRow, error) {
			return q.ListReadyDocuments(ctx)
		})
	if err != nil {
		return nil, err
	}

	var out []Document
	for _, r := range rows {
		d := Document{
			DocID:      r.DocID,
			Title:      r.Title,
			Format:     r.Format,
			SourceKind: r.SourceKind,
			PageCount:  nullInt(r.PageCount),
			ChunkCount: nullInt(r.ChunkCount),
		}
		d.Quality, d.Warnings = summarizeWarnings(r.Warnings)
		out = append(out, d)
	}
	for i := range out {
		heads, err := s.topLevelHeadings(ctx, out[i].DocID)
		if err != nil {
			return nil, err
		}
		out[i].TopHeaders = heads
	}
	return out, nil
}

func (s *Store) topLevelHeadings(ctx context.Context, docID string) ([]string, error) {
	paths, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) ([]pgdbgen.TopLevelHeadingsRow, error) {
			return q.TopLevelHeadings(ctx, docID)
		})
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var out []string
	for _, row := range paths {
		// The query selects the ordinal it orders by, which SELECT DISTINCT
		// requires, so a row rather than a bare string comes back.
		top := topOf(row.HeadingPath)
		if seen[top] || len(out) >= maxTopHeadings {
			continue
		}
		seen[top] = true
		out = append(out, top)
	}
	return out, nil
}

// topOf is the first component of a heading path.
func topOf(path string) string {
	if i := strings.Index(path, " > "); i >= 0 {
		return path[:i]
	}
	return path
}

// -- outline --------------------------------------------------------------

// OutlineEntry is one node of a document's heading tree.
type OutlineEntry struct {
	PageStart *int   `json:"page_start,omitempty"`
	ChunkID   *int64 `json:"chunk_id,omitempty"`
	Heading   string `json:"heading"`
	Section   string `json:"section,omitempty"`
	Depth     int    `json:"depth"`
}

// Outline returns the heading tree of a ready document to the given depth.
func (s *Store) Outline(ctx context.Context, docID string, depth int) ([]OutlineEntry, error) {
	if err := s.requireReady(ctx, docID); err != nil {
		return nil, err
	}
	rows, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) ([]pgdbgen.OutlineRowsRow, error) {
			return q.OutlineRows(ctx, docID)
		})
	if err != nil {
		return nil, err
	}

	// seen is shared across rows, so a heading several chunks sit under is
	// placed by the first of them and not again.
	seen := map[string]bool{}
	var out []OutlineEntry
	for _, r := range rows {
		out = append(out, ancestry(r, depth, seen)...)
	}
	return out, nil
}

// ancestry is the nodes one chunk contributes to the tree: every ancestor of
// its heading path no earlier chunk has placed, down to depth.
//
// The deepest node carries the chunk, so a caller can read the section it
// names and ask for its text; the ancestors above it are headings only.
func ancestry(r pgdbgen.OutlineRowsRow, depth int, seen map[string]bool) []OutlineEntry {
	if r.HeadingPath == "" {
		return nil
	}
	parts := strings.Split(r.HeadingPath, " > ")
	var out []OutlineEntry
	for d := 1; d <= len(parts) && d <= depth; d++ {
		key := strings.Join(parts[:d], " > ")
		if seen[key] {
			continue
		}
		seen[key] = true
		e := OutlineEntry{
			Heading: parts[d-1], Depth: d, PageStart: nullInt(r.PageStart),
		}
		if d == len(parts) {
			e.Section = r.Section.String
			chunkID := r.ID
			e.ChunkID = &chunkID
		}
		out = append(out, e)
	}
	return out
}

func (s *Store) requireReady(ctx context.Context, docID string) error {
	status, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) (string, error) {
			return q.DocumentStatus(ctx, docID)
		})
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no ready document %q", ErrNotFound, docID)
	}
	if err != nil {
		return err
	}
	if status != "ready" {
		// Deliberately the same shape as "does not exist": a document being
		// ingested is not visible, and saying "it exists but isn't ready"
		// would leak it.
		return fmt.Errorf("%w: no ready document %q", ErrNotFound, docID)
	}
	return nil
}
