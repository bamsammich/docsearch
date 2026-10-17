// Package v1import moves a v1 library into v2 by re-ingesting what it held.
//
// A v1 index is a SQLite file, and its chunks were cut by the Python
// pipeline v2 retired. Copying them across would carry v1's extraction into
// a store that is otherwise all v2's, so nothing but the list of sources is
// read: each document is queued again from the file or site it came from,
// and v2's own pipeline reads it. The title is kept, since a person may have
// chosen it.
//
// The file is opened read-only. An import that went wrong leaves the v1
// library as it found it, so the operator can try again or go back.
package v1import

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // the driver a v1 file is read through, in pure Go

	"github.com/bamsammich/docsearch/internal/service/job"
)

// ready is the status v1 gave a document whose ingest finished.
const ready = "ready"

// Document is one row of v1's documents table, as much of it as an import
// needs.
type Document struct {
	Source string
	Title  string
	Status string
}

// Read lists every document in the v1 index at path.
func Read(ctx context.Context, path string) ([]Document, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	documents, err := read(ctx, db)
	return documents, errors.Join(err, db.Close())
}

func read(ctx context.Context, db *sql.DB) ([]Document, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT source_path, title, status FROM documents ORDER BY source_path`)
	if err != nil {
		return nil, fmt.Errorf("read v1 documents: %w", err)
	}
	var documents []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.Source, &d.Title, &d.Status); err != nil {
			return nil, errors.Join(fmt.Errorf("read v1 document: %w", err), rows.Close())
		}
		documents = append(documents, d)
	}
	return documents, errors.Join(rows.Err(), rows.Close())
}

// RootMap rewrites a v1 file path to where the same file sits for v2. A v1
// path names the library as v1's process saw it, which in a container is a
// mount point, and v2 may mount the same directory somewhere else.
type RootMap struct {
	From string
	To   string
}

// ParseRootMap reads FROM=TO.
func ParseRootMap(spelling string) (RootMap, error) {
	from, to, ok := strings.Cut(spelling, "=")
	if !ok || from == "" || to == "" {
		return RootMap{}, fmt.Errorf("root map %q: write it as FROM=TO", spelling)
	}
	return RootMap{From: filepath.Clean(from), To: filepath.Clean(to)}, nil
}

// Entry is one document's place in an import: queued from Source, or
// skipped for Skipped's reason.
type Entry struct {
	Source  string
	Title   string
	Skipped string
}

// Plan decides what each v1 document becomes. A document v1 never finished
// is skipped rather than retried, because whatever stopped it is still in the
// file; the operator can add it by hand once they know why.
func Plan(documents []Document, roots []RootMap) []Entry {
	entries := make([]Entry, 0, len(documents))
	for _, d := range documents {
		entry := Entry{Source: rewrite(d.Source, roots), Title: d.Title}
		if d.Status != ready {
			entry.Skipped = fmt.Sprintf("v1 left it %s", d.Status)
		}
		entries = append(entries, entry)
	}
	return entries
}

// rewrite moves a file path from one root to another. A site's URL, and a
// path under no mapped root, pass through unchanged.
func rewrite(source string, roots []RootMap) string {
	if parsed, err := url.Parse(source); err == nil && parsed.Scheme != "" {
		return source
	}
	for _, root := range roots {
		rel, err := filepath.Rel(root.From, source)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		return filepath.Join(root.To, rel)
	}
	return source
}

// Enqueuer queues a source for v2's worker.
type Enqueuer interface {
	Enqueue(ctx context.Context, target, title string) ([]job.Queued, error)
}

// Outcome is what happened to one entry.
type Outcome struct {
	Entry
	Err  error
	Jobs []job.Queued
}

// Run queues every entry that was not skipped. One source v2 refuses does not
// stop the rest: an import is a list, and the operator needs the whole of it
// to see what to fix.
func Run(ctx context.Context, enqueuer Enqueuer, entries []Entry) []Outcome {
	outcomes := make([]Outcome, 0, len(entries))
	for _, entry := range entries {
		outcome := Outcome{Entry: entry}
		if entry.Skipped == "" {
			outcome.Jobs, outcome.Err = enqueuer.Enqueue(ctx, entry.Source, entry.Title)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}
