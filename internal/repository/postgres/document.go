package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/store/pgdbgen"
)

// ErrNotFound reports a document the index does not hold.
var ErrNotFound = errors.New("no such document")

// Documents reads stored documents for verification and reporting.
type Documents struct {
	db *sql.DB
	q  *pgdbgen.Queries
	// userID is the library every statement here reads and writes. Bound
	// once, because a repository serves one request and a request belongs
	// to one user.
	userID string
}

// NewDocuments reads through db.
func NewDocuments(db *sql.DB, userID string) *Documents {
	return &Documents{db: db, q: pgdbgen.New(db), userID: userID}
}

// Get is one document whatever its status, because verification has to be
// able to look at a document that never became ready.
func (d *Documents) Get(ctx context.Context, docID string) (*document.Document, error) {
	row, err := read(ctx, d.db, d.q, d.userID,
		func(q *pgdbgen.Queries) (pgdbgen.DocumentByIDRow, error) {
			return q.DocumentByID(ctx, docID)
		})
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
		SourceKind: sourceKind(row.SourceKind),
		Status:     row.Status,
	}
	doc.Quality, doc.Warnings = summarize(row.Warnings)
	return &doc, nil
}

// List is every ready document.
func (d *Documents) List(ctx context.Context) ([]document.Document, error) {
	rows, err := read(ctx, d.db, d.q, d.userID,
		func(q *pgdbgen.Queries) ([]pgdbgen.ListReadyDocumentsRow, error) {
			return q.ListReadyDocuments(ctx)
		})
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
			SourceKind: sourceKind(row.SourceKind),
			Status:     "ready",
		}
		doc.Quality, doc.Warnings = summarize(row.Warnings)
		out = append(out, doc)
	}
	return out, nil
}

// Chunks are a document's chunks in ordinal order.
func (d *Documents) Chunks(ctx context.Context, docID string) ([]domain.Chunk, error) {
	rows, err := read(ctx, d.db, d.q, d.userID,
		func(q *pgdbgen.Queries) ([]pgdbgen.DocumentChunksRow, error) {
			return q.DocumentChunks(ctx, docID)
		})
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
	sections, err := read(ctx, d.db, d.q, d.userID,
		func(q *pgdbgen.Queries) ([]string, error) {
			return q.IndexTermSections(ctx, docID)
		})
	if err != nil {
		return nil, fmt.Errorf("read the index terms of %s: %w", docID, err)
	}
	return sections, nil
}

// IndexTermCount is how many entries a back-of-book index holds, which is
// more than the number of sections they point at.
func (d *Documents) IndexTermCount(ctx context.Context, docID string) (int, error) {
	count, err := read(ctx, d.db, d.q, d.userID,
		func(q *pgdbgen.Queries) (int64, error) {
			return q.IndexTermCount(ctx, docID)
		})
	if err != nil {
		return 0, fmt.Errorf("count the index terms of %s: %w", docID, err)
	}
	return int(count), nil
}

// SectionHasChunks reports whether a section, or any section beneath it,
// holds a chunk.
func (d *Documents) SectionHasChunks(
	ctx context.Context,
	docID, section string,
) (bool, error) {
	found, err := read(ctx, d.db, d.q, d.userID, func(q *pgdbgen.Queries) (bool, error) {
		return q.SectionHasChunks(ctx, pgdbgen.SectionHasChunksParams{
			DocID: docID,
			// One placeholder, used twice in the clause: the subtree test
			// compares the section and then its dotted prefix.
			Section: nullString(section),
		})
	})
	if err != nil {
		return false, fmt.Errorf("resolve section %s of %s: %w", section, docID, err)
	}
	return found, nil
}

// Delete removes every trace of a document, in one transaction.
func (d *Documents) Delete(ctx context.Context, docID string) error {
	return session(ctx, d.db, d.q, d.userID, func(q *pgdbgen.Queries) error {
		return deleteRows(ctx, q, docID)
	})
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

// sourceKind reads the kind a document was stored with. An index written by
// a build that knew a kind this one does not reports unset rather than a
// kind nobody chose.
func sourceKind(stored string) domain.SourceKind {
	var kind domain.SourceKind
	if err := kind.UnmarshalText([]byte(stored)); err != nil {
		return 0
	}
	return kind
}

func stringOf(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func intOf(v sql.NullInt32) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int32)
	return &n
}
