package store

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/bamsammich/docsearch/internal/store/dbgen"
)

// Every generated query is executed against a real database.
//
// Compilation proves nothing about a generated query: sqlc emits the SQL as a
// string constant and the argument list separately, so the two can disagree
// and still build. That is not hypothetical -- `BETWEEN ? AND ?` kept its
// placeholders in the emitted SQL while sqlc counted no parameters for them,
// and the generated call passed one argument for a statement wanting three.
// Only running the statement catches it.
//
// Results are not asserted here; the store methods own their behaviour. This
// asserts that every statement is well-formed and that its argument count
// matches, which is exactly the class of defect generation introduces.
func TestEveryGeneratedQueryExecutes(t *testing.T) {
	st := buildIndex(t)
	t.Cleanup(func() { _ = st.Close() })
	q := dbgen.New(st.db)
	ctx := context.Background()

	res, err := q.EnqueueJob(ctx, dbgen.EnqueueJobParams{
		SourcePath: "/lib/x.pdf",
		Title:      sql.NullString{String: "X", Valid: true},
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	jobID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}

	// Each entry runs one generated query. A nil error is the assertion.
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"AllChunksInOrder", func() error { _, err := q.AllChunksInOrder(ctx, "big"); return err }},
		{"ChunkOrdinal", func() error {
			_, err := q.ChunkOrdinal(ctx, dbgen.ChunkOrdinalParams{ID: 1, DocID: "big"})
			return err
		}},
		{"ChunksInSection", func() error {
			_, err := q.ChunksInSection(ctx, dbgen.ChunksInSectionParams{
				DocID:   "big",
				Section: sql.NullString{String: "1.1", Valid: true},
				Column3: sql.NullString{String: "1.1", Valid: true},
			})
			return err
		}},
		{"ChunksMatchingHeading", func() error {
			_, err := q.ChunksMatchingHeading(ctx, dbgen.ChunksMatchingHeadingParams{
				DocID: "big", LOWER: "section",
			})
			return err
		}},
		{"ContextChunks", func() error {
			_, err := q.ContextChunks(ctx, dbgen.ContextChunksParams{
				DocID: "big", Ordinal: 0, Ordinal_2: 5,
			})
			return err
		}},
		{"OutlineRows", func() error { _, err := q.OutlineRows(ctx, "big"); return err }},
		{"PagesInRange", func() error {
			_, err := q.PagesInRange(ctx, dbgen.PagesInRangeParams{DocID: "big", Page: 1, Page_2: 9})
			return err
		}},
		{"TopLevelHeadings", func() error { _, err := q.TopLevelHeadings(ctx, "big"); return err }},
		{"DocumentStatus", func() error { _, err := q.DocumentStatus(ctx, "big"); return err }},
		{"ListReadyDocuments", func() error { _, err := q.ListReadyDocuments(ctx); return err }},
		{"SchemaVersion", func() error { _, err := q.SchemaVersion(ctx); return err }},
		{"ActiveJobs", func() error { _, err := q.ActiveJobs(ctx); return err }},
		{"RecentJobs", func() error { _, err := q.RecentJobs(ctx, 10); return err }},
		{"JobByID", func() error { _, err := q.JobByID(ctx, jobID); return err }},
		{"JobStatus", func() error { _, err := q.JobStatus(ctx, jobID); return err }},
		{"QueuePosition", func() error { _, err := q.QueuePosition(ctx, jobID); return err }},
		{"RequestJobCancel", func() error { return q.RequestJobCancel(ctx, jobID) }},

		// The ingest writes. internal/repository/sqlite owns what they mean;
		// running them here keeps one place that proves every generated
		// statement is well-formed.
		{"ReadyDocumentWithDigest", func() error {
			_, err := q.ReadyDocumentWithDigest(ctx, "big")
			return err
		}},
		{"CountDocumentChunks", func() error {
			_, err := q.CountDocumentChunks(ctx, "big")
			return err
		}},
		{"DocIDForSourcePath", func() error {
			_, err := q.DocIDForSourcePath(ctx, "/x")
			return err
		}},
		{"DocIDsWithPrefix", func() error { _, err := q.DocIDsWithPrefix(ctx, "b%"); return err }},
		{"InsertDocument", func() error {
			return q.InsertDocument(ctx, dbgen.InsertDocumentParams{
				DocID: "fresh", Title: "Fresh", Format: "markdown",
				SourcePath: "/lib/fresh.md", SourceKind: "file", Sha256: "fresh",
			})
		}},
		{"InsertChunk", func() error {
			return q.InsertChunk(ctx, dbgen.InsertChunkParams{
				DocID: "fresh", Ordinal: 0, Kind: "prose",
				HeadingPath: "Fresh", Text: "text",
			})
		}},
		{"UpsertPage", func() error {
			return q.UpsertPage(ctx, dbgen.UpsertPageParams{
				DocID: "fresh", Page: 1, Text: "page",
			})
		}},
		{"InsertIndexTerm", func() error {
			return q.InsertIndexTerm(ctx, dbgen.InsertIndexTermParams{
				DocID: "fresh", Term: "dimmer", Section: "4.1",
			})
		}},
		{"MarkDocumentReady", func() error {
			return q.MarkDocumentReady(ctx, dbgen.MarkDocumentReadyParams{
				ChunkCount: sql.NullInt64{Int64: 1, Valid: true},
				IngestedAt: sql.NullString{String: "2026-09-20T09:30:00Z", Valid: true},
				DocID:      "fresh",
			})
		}},
		{"CompleteJob", func() error {
			return q.CompleteJob(ctx, dbgen.CompleteJobParams{
				DocID: sql.NullString{String: "fresh", Valid: true}, ID: jobID,
			})
		}},
		{"DeleteDocumentChunks", func() error { return q.DeleteDocumentChunks(ctx, "fresh") }},
		{"DeleteDocumentPages", func() error { return q.DeleteDocumentPages(ctx, "fresh") }},
		{"DeleteDocumentIndexTerms", func() error {
			return q.DeleteDocumentIndexTerms(ctx, "fresh")
		}},
		{"DeleteDocumentRow", func() error { return q.DeleteDocumentRow(ctx, "fresh") }},
	} {
		if err := tc.run(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// A query added to internal/store/query without a case in the table above
// would go unexercised, so the count is asserted rather than trusted.
func TestGeneratedQueryCoverageIsComplete(t *testing.T) {
	// The table above, plus EnqueueJob which runs ahead of it.
	const exercised = 32
	// Every exported method on *Queries is a generated query, except WithTx.
	total := reflect.TypeFor[*dbgen.Queries]().NumMethod()
	if got := total - 1; got != exercised {
		t.Fatalf("dbgen exposes %d queries but TestEveryGeneratedQueryExecutes runs %d; "+
			"add the new query to that table", got, exercised)
	}
}
