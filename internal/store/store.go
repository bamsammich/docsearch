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

	_ "modernc.org/sqlite" // pure-Go driver; FTS5 is compiled in, no build tag
)

// ErrNotFound is returned when a requested document or job does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the shared SQLite database.
type Store struct{ db *sql.DB }

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
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// RequiredSchemaVersion is the schema this binary was built against. It must
// track docsearch.db.SCHEMA_VERSION on the Python side.
//
// A version is checked rather than a set of columns because the two catch
// different faults. Column presence catches an *added* column. It cannot catch
// changed semantics on an existing one -- index_terms.section holding section
// numbers where it once held page numbers passes every structural check while
// silently changing what the index-term boost resolves to. Only a version
// number, bumped deliberately, catches that.
const RequiredSchemaVersion = 3

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

	var version int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version LIMIT 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return &ErrSchemaVersion{Found: 0, Required: RequiredSchemaVersion}
	}
	if err != nil {
		// No schema_version table at all: a database from before versioning.
		return &ErrSchemaVersion{Found: 0, Required: RequiredSchemaVersion}
	}
	if version != RequiredSchemaVersion {
		return &ErrSchemaVersion{Found: version, Required: RequiredSchemaVersion}
	}
	return nil
}

// -- documents ------------------------------------------------------------

// Document is a ready document as reported by list_documents.
type Document struct {
	DocID      string   `json:"doc_id"`
	Title      string   `json:"title"`
	Format     string   `json:"format"`
	PageCount  *int     `json:"page_count,omitempty"`
	ChunkCount *int     `json:"chunk_count,omitempty"`
	Quality    string   `json:"quality"`
	Warnings   []string `json:"warnings,omitempty"`
	TopHeaders []string `json:"top_level_headings,omitempty"`
}

const maxTopHeadings = 15

// ListDocuments returns every ready document with its top-level headings.
func (s *Store) ListDocuments(ctx context.Context) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT doc_id, title, format, page_count, chunk_count, warnings
		  FROM documents
		 WHERE status = 'ready'
		 ORDER BY doc_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Document
	for rows.Next() {
		var d Document
		var warnings sql.NullString
		if err := rows.Scan(&d.DocID, &d.Title, &d.Format, &d.PageCount, &d.ChunkCount,
			&warnings); err != nil {
			return nil, err
		}
		d.Quality, d.Warnings = summarizeWarnings(warnings)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT heading_path FROM chunks
		 WHERE doc_id = ? AND heading_path <> ''
		 ORDER BY ordinal`, docID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
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
	return out, rows.Err()
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, section, page_start, heading_path
		  FROM chunks WHERE doc_id = ? ORDER BY ordinal`, docID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	seen := map[string]bool{}
	var out []OutlineEntry
	for rows.Next() {
		var id int64
		var section sql.NullString
		var pageStart sql.NullInt64
		var path string
		if err := rows.Scan(&id, &section, &pageStart, &path); err != nil {
			return nil, err
		}
		if path == "" {
			continue
		}
		parts := strings.Split(path, " > ")
		for d := 1; d <= len(parts) && d <= depth; d++ {
			key := strings.Join(parts[:d], " > ")
			if seen[key] {
				continue
			}
			seen[key] = true
			e := OutlineEntry{Heading: parts[d-1], Depth: d}
			if d == len(parts) {
				if section.Valid {
					e.Section = section.String
				}
				cid := id
				e.ChunkID = &cid
			}
			if pageStart.Valid {
				p := int(pageStart.Int64)
				e.PageStart = &p
			}
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

func (s *Store) requireReady(ctx context.Context, docID string) error {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM documents WHERE doc_id = ?`, docID).Scan(&status)
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
