package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/store/dbgen"
)

// ErrNotFound reports a document the index does not hold.
var ErrNotFound = errors.New("no such document")

// Documents reads stored documents for verification and reporting.
type Documents struct {
	db *sql.DB
	q  *dbgen.Queries
}

// NewDocuments reads through db.
func NewDocuments(db *sql.DB) *Documents {
	return &Documents{db: db, q: dbgen.New(db)}
}

// Get is one document whatever its status, because verification has to be
// able to look at a document that never became ready.
func (d *Documents) Get(ctx context.Context, docID string) (*document.Document, error) {
	row, err := d.q.DocumentByID(ctx, docID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, docID)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", docID, err)
	}
	doc := document.Document{
		PageCount:  intOf(row.PageCount),
		ChunkCount: intOf(row.ChunkCount),
		DocID:      row.DocID,
		Title:      row.Title,
		Format:     row.Format,
		SourceKind: row.SourceKind,
		Status:     row.Status,
	}
	doc.Quality, doc.Warnings = summarize(row.Warnings)
	return &doc, nil
}

// List is every ready document.
func (d *Documents) List(ctx context.Context) ([]document.Document, error) {
	rows, err := d.q.ListReadyDocuments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	out := make([]document.Document, 0, len(rows))
	for _, row := range rows {
		doc := document.Document{
			PageCount:  intOf(row.PageCount),
			ChunkCount: intOf(row.ChunkCount),
			DocID:      row.DocID,
			Title:      row.Title,
			Format:     row.Format,
			SourceKind: row.SourceKind,
			Status:     "ready",
		}
		doc.Quality, doc.Warnings = summarize(row.Warnings)
		out = append(out, doc)
	}
	return out, nil
}

// Chunks are a document's chunks in ordinal order.
func (d *Documents) Chunks(ctx context.Context, docID string) ([]domain.Chunk, error) {
	rows, err := d.q.DocumentChunks(ctx, docID)
	if err != nil {
		return nil, fmt.Errorf("read the chunks of %s: %w", docID, err)
	}
	out := make([]domain.Chunk, 0, len(rows))
	for _, row := range rows {
		var kind domain.ChunkKind
		if err := kind.UnmarshalText([]byte(row.Kind)); err != nil {
			return nil, fmt.Errorf("chunk %d of %s: %w", row.Ordinal, docID, err)
		}
		out = append(out, domain.Chunk{
			Section:          stringOf(row.Section),
			PageStart:        intOf(row.PageStart),
			PageEnd:          intOf(row.PageEnd),
			PrintedPageStart: intOf(row.PrintedPageStart),
			URL:              stringOf(row.Url),
			Fragment:         stringOf(row.Fragment),
			HeadingPath:      row.HeadingPath,
			Text:             row.Text,
			Ordinal:          int(row.Ordinal),
			ImageCount:       int(row.ImageCount),
			Kind:             kind,
		})
	}
	return out, nil
}

// IndexTermSections are the distinct sections a back-of-book index points at.
func (d *Documents) IndexTermSections(ctx context.Context, docID string) ([]string, error) {
	sections, err := d.q.IndexTermSections(ctx, docID)
	if err != nil {
		return nil, fmt.Errorf("read the index terms of %s: %w", docID, err)
	}
	return sections, nil
}

// SectionHasChunks reports whether a section, or any section beneath it,
// holds a chunk.
func (d *Documents) SectionHasChunks(
	ctx context.Context,
	docID, section string,
) (bool, error) {
	found, err := d.q.SectionHasChunks(ctx, dbgen.SectionHasChunksParams{
		DocID:   docID,
		Section: nullString(section),
		Column3: nullString(section),
	})
	if err != nil {
		return false, fmt.Errorf("resolve section %s of %s: %w", section, docID, err)
	}
	return found, nil
}

// Delete removes every trace of a document, in one transaction.
func (d *Documents) Delete(ctx context.Context, docID string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := deleteRows(ctx, d.q.WithTx(tx), docID); err != nil {
		return errors.Join(err, rollback(tx))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// summarize reads the grade and the notes out of a stored structure report.
//
// A report that cannot be parsed is not a reason to refuse the document: the
// rows are still there, and the grade is simply unknown.
func summarize(warnings sql.NullString) (domain.Quality, []string) {
	if !warnings.Valid || warnings.String == "" {
		return 0, nil
	}
	var stored struct {
		Quality string   `json:"quality"`
		Notes   []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(warnings.String), &stored); err != nil {
		return 0, nil
	}
	var quality domain.Quality
	if err := quality.UnmarshalText([]byte(stored.Quality)); err != nil {
		return 0, stored.Notes
	}
	return quality, stored.Notes
}

func stringOf(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func intOf(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}
