// Package sqlite stores an ingest in the SQLite index the server reads.
//
// Each method is one transaction, because that is what the ingest service
// asks of a repository: it decides what happens, and the repository decides
// what happens atomically. Nothing here decides anything about a document.
//
// Postgres arrives in phase 04 behind the same port, so the transaction
// boundaries and the ordering rules are written down here rather than left
// to the caller to repeat.
//
// Ported from python/docsearch/ingest.py and db.py.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver; the server opens the same file

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/store/dbgen"
)

// timeFormat is how the Python pipeline writes a timestamp, and what every
// reader of documents.ingested_at expects.
const timeFormat = "2006-01-02T15:04:05Z"

// Repository writes documents to one SQLite database.
type Repository struct {
	db *sql.DB
	q  *dbgen.Queries
}

// New writes through db, which the caller opens and closes. Open it with
// this package's Open, or with a DSN asking for the same pragmas.
func New(db *sql.DB) *Repository {
	return &Repository{db: db, q: dbgen.New(db)}
}

// Open connects to an index for writing.
//
// _txlock=immediate is what makes every transaction here take its write lock
// at BEGIN. A deferred transaction that upgrades to a write part way through
// cannot wait for the other writer under WAL: it fails at once rather than
// blocking on busy_timeout, and the ingest would die on a lock a retry would
// have got. The Python worker asks for the same thing by writing BEGIN
// IMMEDIATE itself.
//
// busy_timeout is not optional either: the server reads while the worker
// holds brief write transactions, and both processes have to agree.
func Open(path string) (*sql.DB, error) {
	dsn := path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_txlock=immediate" +
		"&_time_format=sqlite"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("open %s: %w", path, err), db.Close())
	}
	return db, nil
}

func (r *Repository) ReadyWithDigest(
	ctx context.Context,
	digest string,
) (*ingest.Existing, error) {
	row, err := r.q.ReadyDocumentWithDigest(ctx, digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no document holds these bytes, which is not a failure
	}
	if err != nil {
		return nil, fmt.Errorf("read the document with hash %s: %w", digest, err)
	}
	chunks, err := r.q.CountDocumentChunks(ctx, row.DocID)
	if err != nil {
		return nil, fmt.Errorf("count the chunks of %s: %w", row.DocID, err)
	}
	return &ingest.Existing{
		DocID:      row.DocID,
		Title:      row.Title,
		ChunkCount: int(chunks),
	}, nil
}

func (r *Repository) DocIDForIdentity(ctx context.Context, identity string) (string, error) {
	docID, err := r.q.DocIDForSourcePath(ctx, identity)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the document at %s: %w", identity, err)
	}
	return docID, nil
}

// DocIDsWithPrefix escapes the prefix, so a title slugifying to something
// holding a percent sign does not match every identifier in the index.
func (r *Repository) DocIDsWithPrefix(ctx context.Context, prefix string) ([]string, error) {
	ids, err := r.q.DocIDsWithPrefix(ctx, likePrefix(prefix))
	if err != nil {
		return nil, fmt.Errorf("read identifiers beginning %q: %w", prefix, err)
	}
	return ids, nil
}

// Create writes the document as ingesting. Replacing one deletes its rows in
// the same transaction, so a failure here cannot leave two documents at one
// identity.
func (r *Repository) Create(ctx context.Context, doc ingest.Document, replacing string) error {
	return r.inTx(ctx, func(q *dbgen.Queries) error {
		if replacing != "" {
			if err := deleteRows(ctx, q, replacing); err != nil {
				return err
			}
		}
		return q.InsertDocument(ctx, dbgen.InsertDocumentParams{
			DocID:      doc.DocID,
			Title:      doc.Title,
			Format:     doc.Format,
			SourcePath: doc.Identity,
			SourceKind: doc.Kind.String(),
			Sha256:     doc.Digest,
			PageCount:  nullInt64(doc.PageCount),
		})
	})
}

func (r *Repository) AddChunks(
	ctx context.Context,
	docID string,
	chunks []domain.Chunk,
) error {
	return r.inTx(ctx, func(q *dbgen.Queries) error {
		for _, c := range chunks {
			if err := q.InsertChunk(ctx, chunkParams(docID, c)); err != nil {
				return fmt.Errorf("write chunk %d: %w", c.Ordinal, err)
			}
		}
		return nil
	})
}

func (r *Repository) AddPagesAndTerms(
	ctx context.Context,
	docID string,
	pages map[int]string,
	terms [][2]string,
) error {
	if len(pages) == 0 && len(terms) == 0 {
		return nil
	}
	return r.inTx(ctx, func(q *dbgen.Queries) error {
		if err := writePages(ctx, q, docID, pages); err != nil {
			return err
		}
		return writeTerms(ctx, q, docID, terms)
	})
}

// writePages writes a paginated format's page text, in page order, so a
// database dumped twice compares equal.
func writePages(
	ctx context.Context,
	q *dbgen.Queries,
	docID string,
	pages map[int]string,
) error {
	for _, page := range slices.Sorted(maps.Keys(pages)) {
		err := q.UpsertPage(ctx, dbgen.UpsertPageParams{
			DocID: docID,
			Page:  int64(page),
			Text:  pages[page],
		})
		if err != nil {
			return fmt.Errorf("write page %d: %w", page, err)
		}
	}
	return nil
}

// writeTerms writes a back-of-book index's terms, in the order the adapter
// found them.
func writeTerms(
	ctx context.Context,
	q *dbgen.Queries,
	docID string,
	terms [][2]string,
) error {
	for _, term := range terms {
		err := q.InsertIndexTerm(ctx, dbgen.InsertIndexTermParams{
			DocID:   docID,
			Term:    term[0],
			Section: term[1],
		})
		if err != nil {
			return fmt.Errorf("write index term %q: %w", term[0], err)
		}
	}
	return nil
}

// MarkReady makes the document visible to search, and completes the job that
// wrote it in the same transaction.
func (r *Repository) MarkReady(ctx context.Context, ready ingest.Ready) error {
	return r.inTx(ctx, func(q *dbgen.Queries) error {
		warnings := nullString(string(ready.Warnings))
		err := q.MarkDocumentReady(ctx, dbgen.MarkDocumentReadyParams{
			ChunkCount: sql.NullInt64{Int64: int64(ready.ChunkCount), Valid: true},
			IngestedAt: nullString(ready.IngestedAt.UTC().Format(timeFormat)),
			Warnings:   warnings,
			DocID:      ready.DocID,
		})
		if err != nil {
			return fmt.Errorf("publish %s: %w", ready.DocID, err)
		}
		if ready.JobID == nil {
			return nil
		}
		return q.CompleteJob(ctx, dbgen.CompleteJobParams{
			DocID:    nullString(ready.DocID),
			Warnings: warnings,
			ID:       *ready.JobID,
		})
	})
}

func (r *Repository) Delete(ctx context.Context, docID string) error {
	return r.inTx(ctx, func(q *dbgen.Queries) error { return deleteRows(ctx, q, docID) })
}

// deleteRows removes every trace of a document.
//
// Chunks go first and go through SQL. The AFTER DELETE trigger on chunks is
// what clears chunks_fts, and deleting the document row first would take the
// chunks with it through the foreign key, leaving the full-text index holding
// rows for a document that no longer exists.
func deleteRows(ctx context.Context, q *dbgen.Queries, docID string) error {
	if err := q.DeleteDocumentChunks(ctx, docID); err != nil {
		return fmt.Errorf("delete the chunks of %s: %w", docID, err)
	}
	if err := q.DeleteDocumentPages(ctx, docID); err != nil {
		return fmt.Errorf("delete the pages of %s: %w", docID, err)
	}
	if err := q.DeleteDocumentIndexTerms(ctx, docID); err != nil {
		return fmt.Errorf("delete the index terms of %s: %w", docID, err)
	}
	if err := q.DeleteDocumentRow(ctx, docID); err != nil {
		return fmt.Errorf("delete %s: %w", docID, err)
	}
	return nil
}

// inTx runs write inside one transaction, rolling back whatever it wrote if
// it fails. The transaction takes its write lock at BEGIN, which is what
// Open's _txlock=immediate asks the driver for.
func (r *Repository) inTx(ctx context.Context, write func(*dbgen.Queries) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := write(r.q.WithTx(tx)); err != nil {
		return errors.Join(err, rollback(tx))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// rollback reports a rollback that itself failed, and says nothing about one
// the driver already performed.
func rollback(tx *sql.Tx) error {
	err := tx.Rollback()
	if err == nil || errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return fmt.Errorf("roll back: %w", err)
}

// chunkParams is one chunk as its row.
func chunkParams(docID string, c domain.Chunk) dbgen.InsertChunkParams {
	return dbgen.InsertChunkParams{
		DocID:            docID,
		Ordinal:          int64(c.Ordinal),
		Section:          nullStringOf(c.Section),
		PageStart:        nullInt64(c.PageStart),
		PageEnd:          nullInt64(c.PageEnd),
		PrintedPageStart: nullInt64(c.PrintedPageStart),
		ImageCount:       int64(c.ImageCount),
		Kind:             c.Kind.String(),
		Url:              nullStringOf(c.URL),
		Fragment:         nullStringOf(c.Fragment),
		HeadingPath:      c.HeadingPath,
		Text:             c.Text,
	}
}

// likePrefix escapes a LIKE pattern's wildcards, so a slug holding one
// matches literally.
func likePrefix(prefix string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
	return escaped + "%"
}

func nullInt64(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

func nullStringOf(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}

func nullString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}
