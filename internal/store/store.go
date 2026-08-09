// Package store is the server's data access layer.
//
// The server is a reader for document data and a writer for ingest_jobs, so
// the database is NOT opened read-only. WAL mode and a busy timeout are
// mandatory and shared with the worker: the worker writes chunks while the
// server serves queries, and rollback-journal mode locks them against each
// other.
//
// Every read path filters documents.status='ready'. A document is written
// incrementally and must not appear in any result until its ingest completes.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/bamsammich/docsearch/internal/store/dbgen"

	_ "modernc.org/sqlite" // pure-Go driver; FTS5 is compiled in, no build tag
)

// ErrNotFound is returned when a requested document or job does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the shared SQLite database.
//
// q holds the queries generated from python/docsearch/schema.sql. The two
// statements it cannot express -- Search, which composes its WHERE clause from
// the request and calls bm25(), and matchingIndexSections, which builds one
// term per query word -- run through db directly.
type Store struct {
	db *sql.DB
	q  *dbgen.Queries
}

// Open connects to path with the pragmas both processes must agree on.
//
// busy_timeout is not optional: the worker holds brief write transactions
// while committing chunk batches, and without it a concurrent read fails
// immediately with SQLITE_BUSY instead of waiting.
func Open(path string) (*Store, error) {
	dsn := path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_time_format=sqlite"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, q: dbgen.New(db)}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// nullInt converts a nullable SQLite integer to the *int the tool schemas
// use, where absent means "not applicable to this document" rather than zero.
func nullInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

// RequiredSchemaVersion is the schema this binary was built against. It must
// equal docsearch.db.SCHEMA_VERSION, which is where the number is chosen;
// tests/test_schema_version_agreement.py fails when they diverge.
//
// A version is checked rather than a set of columns because the two catch
// different faults. Column presence catches an *added* column. It cannot catch
// changed semantics on an existing one -- index_terms.section holding section
// numbers where it once held page numbers passes every structural check while
// silently changing what the index-term boost resolves to. Only a version
// number, bumped deliberately, catches that.
const RequiredSchemaVersion = 5

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
	for _, name := range []string{
		"documents", "ingest_jobs", "chunks", "chunks_fts", "pages", "index_terms",
	} {
		var found string
		err := s.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name = ?`,
			name).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("schema incomplete: %s is missing", name)
		}
		if err != nil {
			return fmt.Errorf("schema check failed")
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
	rows, err := s.q.ListReadyDocuments(ctx)
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
	paths, err := s.q.TopLevelHeadings(ctx, docID)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var out []string
	for _, path := range paths {
		top := path
		if i := strings.Index(path, " > "); i >= 0 {
			top = path[:i]
		}
		if !seen[top] {
			seen[top] = true
			out = append(out, top)
			if len(out) >= maxTopHeadings {
				break
			}
		}
	}
	return out, nil
}

// -- outline --------------------------------------------------------------

// OutlineEntry is one node of a document's heading tree.
type OutlineEntry struct {
	Heading   string `json:"heading"`
	Depth     int    `json:"depth"`
	Section   string `json:"section,omitempty"`
	PageStart *int   `json:"page_start,omitempty"`
	ChunkID   *int64 `json:"chunk_id,omitempty"`
}

// Outline returns the heading tree of a ready document to the given depth.
func (s *Store) Outline(ctx context.Context, docID string, depth int) ([]OutlineEntry, error) {
	if err := s.requireReady(ctx, docID); err != nil {
		return nil, err
	}
	rows, err := s.q.OutlineRows(ctx, docID)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var out []OutlineEntry
	for _, r := range rows {
		if r.HeadingPath == "" {
			continue
		}
		parts := strings.Split(r.HeadingPath, " > ")
		for d := 1; d <= len(parts) && d <= depth; d++ {
			key := strings.Join(parts[:d], " > ")
			if seen[key] {
				continue
			}
			seen[key] = true
			e := OutlineEntry{Heading: parts[d-1], Depth: d}
			if d == len(parts) {
				if r.Section.Valid {
					e.Section = r.Section.String
				}
				cid := r.ID
				e.ChunkID = &cid
			}
			e.PageStart = nullInt(r.PageStart)
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Store) requireReady(ctx context.Context, docID string) error {
	status, err := s.q.DocumentStatus(ctx, docID)
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
