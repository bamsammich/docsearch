package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/repository/sqlite"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/service/ingest"
)

// DocumentsSuite reads back what an ingest wrote, since what the repository
// is for is whether the rows survive the round trip.
type DocumentsSuite struct {
	suite.Suite
	docs  *sqlite.Documents
	write *sqlite.Repository
	db    *sql.DB
}

func TestDocuments(t *testing.T) { suite.Run(t, new(DocumentsSuite)) }

func (s *DocumentsSuite) SetupTest() {
	db, err := sqlite.Open(filepath.Join(s.T().TempDir(), "index.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })
	s.Require().NoError(schema.Create(s.T().Context(), db))

	s.db = db
	s.docs = sqlite.NewDocuments(db)
	s.write = sqlite.New(db)
}

// ingested writes one document with the chunks given and publishes it.
func (s *DocumentsSuite) ingested(docID string, chunks []domain.Chunk, warnings string) {
	s.Require().NoError(s.write.Create(s.T().Context(), ingest.Document{
		DocID: docID, Title: "Manual", Format: "pdf",
		Identity: "/library/" + docID + ".pdf", Digest: docID,
		Kind: domain.SourceKindFile, PageCount: pageCount(6),
	}, ""))
	s.Require().NoError(s.write.AddChunks(s.T().Context(), docID, chunks))
	s.Require().NoError(s.write.MarkReady(s.T().Context(), ingest.Ready{
		IngestedAt: stamped, DocID: docID,
		Warnings: []byte(warnings), ChunkCount: len(chunks),
	}))
}

func pageCount(n int) *int { return &n }

// chunkAt is one chunk with every optional column filled.
func chunkAt(ordinal int, section string, page int) domain.Chunk {
	s, p := section, page
	url, fragment := "https://example.com/docs/install", "packages"
	return domain.Chunk{
		Section:     &s,
		PageStart:   &p,
		PageEnd:     &p,
		URL:         &url,
		Fragment:    &fragment,
		HeadingPath: "Manual > Install",
		Text:        "Packages are published for every release.",
		Ordinal:     ordinal,
		ImageCount:  1,
		Kind:        domain.KindProse,
	}
}

func (s *DocumentsSuite) TestEveryColumnSurvivesTheRoundTrip() {
	s.ingested("manual", []domain.Chunk{chunkAt(0, "4.1", 12)}, `{"quality":"ok"}`)

	chunks, err := s.docs.Chunks(s.T().Context(), "manual")
	s.Require().NoError(err)
	s.Require().Len(chunks, 1)

	got := chunks[0]
	s.Equal("4.1", *got.Section)
	s.Equal(12, *got.PageStart)
	s.Equal("https://example.com/docs/install", *got.URL)
	s.Equal("packages", *got.Fragment)
	s.Equal("Manual > Install", got.HeadingPath)
	s.Equal(domain.KindProse, got.Kind)
	s.Equal(1, got.ImageCount)
}

func (s *DocumentsSuite) TestADocumentCarriesItsGradeAndNotes() {
	s.ingested("manual", []domain.Chunk{chunkAt(0, "1", 1)},
		`{"quality":"degraded","notes":["boundaries came from the token budget"]}`)

	doc, err := s.docs.Get(s.T().Context(), "manual")
	s.Require().NoError(err)
	s.Equal(domain.QualityDegraded, doc.Quality)
	s.Equal([]string{"boundaries came from the token budget"}, doc.Warnings)
	s.Equal(6, *doc.PageCount)
}

func (s *DocumentsSuite) TestAnUnreadableReportLeavesTheDocumentReadable() {
	// The rows are still there; only the grade is unknown.
	s.ingested("manual", []domain.Chunk{chunkAt(0, "1", 1)}, "not json at all")

	doc, err := s.docs.Get(s.T().Context(), "manual")
	s.Require().NoError(err)
	s.Equal("manual", doc.DocID)
	s.Equal(domain.Quality(0), doc.Quality, "unset rather than a grade nobody chose")
}

func (s *DocumentsSuite) TestADocumentTheIndexDoesNotHoldIsNamed() {
	_, err := s.docs.Get(s.T().Context(), "absent")
	s.Require().ErrorIs(err, sqlite.ErrNotFound)
	s.Contains(err.Error(), "absent")
}

func (s *DocumentsSuite) TestAnUnpublishedDocumentIsStillVerifiable() {
	// Verification has to be able to look at a document that never became
	// ready, which is exactly the one worth looking at.
	s.Require().NoError(s.write.Create(s.T().Context(), ingest.Document{
		DocID: "half", Title: "Half", Format: "pdf",
		Identity: "/library/half.pdf", Digest: "half", Kind: domain.SourceKindFile,
	}, ""))

	doc, err := s.docs.Get(s.T().Context(), "half")
	s.Require().NoError(err)
	s.Equal("ingesting", doc.Status)

	listed, err := s.docs.List(s.T().Context())
	s.Require().NoError(err)
	s.Empty(listed, "and it stays out of every list")
}

func (s *DocumentsSuite) TestASectionResolvesItsWholeSubtree() {
	// An index entry pointing at chapter 4 refers to the whole chapter.
	s.ingested("manual", []domain.Chunk{
		chunkAt(0, "4.1", 12),
		chunkAt(1, "4.2", 13),
	}, "")

	found, err := s.docs.SectionHasChunks(s.T().Context(), "manual", "4")
	s.Require().NoError(err)
	s.True(found, "the chapter has no chunk of its own, but its children do")

	absent, err := s.docs.SectionHasChunks(s.T().Context(), "manual", "9")
	s.Require().NoError(err)
	s.False(absent)
}

func (s *DocumentsSuite) TestIndexTermSectionsComeBackDistinctAndSorted() {
	s.ingested("manual", []domain.Chunk{chunkAt(0, "1", 1)}, "")
	s.Require().NoError(s.write.AddPagesAndTerms(s.T().Context(), "manual", nil, [][2]string{
		{"dimmer", "4.1"}, {"patch", "4.1"}, {"universe", "2"},
	}))

	sections, err := s.docs.IndexTermSections(s.T().Context(), "manual")
	s.Require().NoError(err)
	s.Equal([]string{"2", "4.1"}, sections)
}

func (s *DocumentsSuite) TestDeletingADocumentClearsItsFullTextRows() {
	s.ingested("manual", []domain.Chunk{chunkAt(0, "1", 1)}, "")
	s.Require().NoError(s.docs.Delete(s.T().Context(), "manual"))

	var rows int
	s.Require().NoError(s.db.QueryRow(
		`SELECT COUNT(*) FROM chunks_fts WHERE doc_id='manual'`).Scan(&rows))
	s.Zero(rows)

	_, err := s.docs.Get(s.T().Context(), "manual")
	s.Require().ErrorIs(err, sqlite.ErrNotFound)
}
