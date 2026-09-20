package sqlite_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/repository/sqlite"
	"github.com/bamsammich/docsearch/internal/service/ingest"
)

// stamped is the clock every document here is published at.
var stamped = time.Date(2026, 9, 20, 9, 30, 0, 0, time.UTC)

// RepositorySuite runs every write against a real index built from the
// schema the Python pipeline creates databases from.
//
// A repository has no behaviour worth mocking: what it is for is the rows it
// leaves behind and the order it writes them in, and only a database shows
// either.
type RepositorySuite struct {
	suite.Suite
	repo *sqlite.Repository
	db   *sql.DB
}

func TestRepository(t *testing.T) { suite.Run(t, new(RepositorySuite)) }

func (s *RepositorySuite) SetupTest() {
	db, err := sqlite.Open(filepath.Join(s.T().TempDir(), "index.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })

	// The schema itself, read rather than copied, so a migration that
	// changes it fails here rather than drifting.
	schema, err := os.ReadFile("../../../python/docsearch/schema.sql")
	s.Require().NoError(err)
	_, err = db.Exec(string(schema))
	s.Require().NoError(err)

	s.db = db
	s.repo = sqlite.New(db)
}

// document is a document with one chunk, written and published.
func (s *RepositorySuite) document(docID, title, identity, digest string) {
	doc := ingest.Document{
		DocID:    docID,
		Title:    title,
		Format:   "markdown",
		Identity: identity,
		Digest:   digest,
		Kind:     domain.SourceKindFile,
	}
	s.Require().NoError(s.repo.Create(s.T().Context(), doc, ""))
	s.Require().NoError(s.repo.AddChunks(s.T().Context(), docID, []domain.Chunk{chunk(0, "one")}))
	s.Require().NoError(s.repo.MarkReady(s.T().Context(), ingest.Ready{
		IngestedAt: stamped,
		DocID:      docID,
		Warnings:   []byte(`{"quality":"ok"}`),
		ChunkCount: 1,
	}))
}

func chunk(ordinal int, text string) domain.Chunk {
	section := "1"
	return domain.Chunk{
		Section:     &section,
		HeadingPath: "Guide > Install",
		Text:        text,
		Ordinal:     ordinal,
		Kind:        domain.KindProse,
	}
}

// queryOne reads one scalar, for asserting on rows the repository wrote.
func (s *RepositorySuite) queryOne(query string, args ...any) string {
	var value sql.NullString
	s.Require().NoError(s.db.QueryRow(query, args...).Scan(&value))
	return value.String
}

func (s *RepositorySuite) TestADocumentIsInvisibleUntilItIsPublished() {
	// Every read path filters status = 'ready', so a half-written document
	// must never carry that status.
	doc := ingest.Document{
		DocID: "guide", Title: "Guide", Format: "markdown",
		Identity: "/library/guide.md", Digest: "abc", Kind: domain.SourceKindFile,
	}
	s.Require().NoError(s.repo.Create(s.T().Context(), doc, ""))
	s.Equal("ingesting", s.queryOne(`SELECT status FROM documents WHERE doc_id='guide'`))

	s.Require().NoError(s.repo.MarkReady(s.T().Context(), ingest.Ready{
		IngestedAt: stamped, DocID: "guide", ChunkCount: 3,
	}))
	s.Equal("ready", s.queryOne(`SELECT status FROM documents WHERE doc_id='guide'`))
	s.Equal("3", s.queryOne(`SELECT chunk_count FROM documents WHERE doc_id='guide'`))
}

func (s *RepositorySuite) TestTheTimestampIsWrittenAsPythonWritesIt() {
	// Every reader of documents.ingested_at parses this spelling.
	s.document("guide", "Guide", "/library/guide.md", "abc")
	s.Equal(
		"2026-09-20T09:30:00Z",
		s.queryOne(`SELECT ingested_at FROM documents WHERE doc_id='guide'`),
	)
}

func (s *RepositorySuite) TestBytesAlreadyIndexedAreFoundByTheirHash() {
	s.document("guide", "Guide", "/library/guide.md", "abc")

	existing, err := s.repo.ReadyWithDigest(s.T().Context(), "abc")
	s.Require().NoError(err)
	s.Require().NotNil(existing)
	s.Equal("guide", existing.DocID)
	s.Equal("Guide", existing.Title)
	s.Equal(1, existing.ChunkCount)
}

func (s *RepositorySuite) TestAHashNobodyHoldsIsNotAFailure() {
	existing, err := s.repo.ReadyWithDigest(s.T().Context(), "nothing-holds-this")
	s.Require().NoError(err)
	s.Nil(existing)
}

func (s *RepositorySuite) TestAnUnpublishedDocumentDoesNotAnswerForItsHash() {
	// A document still being written holds its hash, and returning it would
	// report an ingest that never finished as already done.
	doc := ingest.Document{
		DocID: "guide", Title: "Guide", Format: "markdown",
		Identity: "/library/guide.md", Digest: "abc", Kind: domain.SourceKindFile,
	}
	s.Require().NoError(s.repo.Create(s.T().Context(), doc, ""))

	existing, err := s.repo.ReadyWithDigest(s.T().Context(), "abc")
	s.Require().NoError(err)
	s.Nil(existing)
}

func (s *RepositorySuite) TestADocumentIsFoundByWhereItWasReadFrom() {
	s.document("guide", "Guide", "/library/guide.md", "abc")

	docID, err := s.repo.DocIDForIdentity(s.T().Context(), "/library/guide.md")
	s.Require().NoError(err)
	s.Equal("guide", docID)

	absent, err := s.repo.DocIDForIdentity(s.T().Context(), "/library/other.md")
	s.Require().NoError(err)
	s.Empty(absent)
}

func (s *RepositorySuite) TestTakenIdentifiersAreListedByPrefix() {
	s.document("guide", "Guide", "/library/a.md", "a")
	s.document("guide-2", "Guide", "/library/b.md", "b")
	s.document("handbook", "Handbook", "/library/c.md", "c")

	ids, err := s.repo.DocIDsWithPrefix(s.T().Context(), "guide")
	s.Require().NoError(err)
	s.ElementsMatch([]string{"guide", "guide-2"}, ids)
}

func (s *RepositorySuite) TestAWildcardInASlugMatchesItself() {
	// A slug cannot hold a percent sign today, but an unescaped LIKE pattern
	// would return every identifier in the index and number a new document
	// past all of them.
	s.document("guide", "Guide", "/library/a.md", "a")

	ids, err := s.repo.DocIDsWithPrefix(s.T().Context(), "%")
	s.Require().NoError(err)
	s.Empty(ids)
}

func (s *RepositorySuite) TestReplacingADocumentLeavesOneSetOfRows() {
	s.document("guide", "Guide", "/library/guide.md", "abc")
	s.Require().NoError(s.repo.AddChunks(s.T().Context(), "guide", []domain.Chunk{
		chunk(1, "two"), chunk(2, "three"),
	}))

	replaced := ingest.Document{
		DocID: "guide", Title: "Guide, second edition", Format: "markdown",
		Identity: "/library/guide.md", Digest: "def", Kind: domain.SourceKindFile,
	}
	s.Require().NoError(s.repo.Create(s.T().Context(), replaced, "guide"))

	s.Equal("0", s.queryOne(`SELECT COUNT(*) FROM chunks WHERE doc_id='guide'`))
	s.Equal("1", s.queryOne(`SELECT COUNT(*) FROM documents WHERE doc_id='guide'`))
	s.Equal("def", s.queryOne(`SELECT sha256 FROM documents WHERE doc_id='guide'`))
}

func (s *RepositorySuite) TestDeletingADocumentClearsItsFullTextRows() {
	// The AFTER DELETE trigger on chunks is what clears chunks_fts, so the
	// chunks have to be deleted before the document row takes them through
	// the foreign key.
	s.document("guide", "Guide", "/library/guide.md", "abc")
	s.Equal("1", s.queryOne(`SELECT COUNT(*) FROM chunks_fts WHERE doc_id='guide'`))

	s.Require().NoError(s.repo.Delete(s.T().Context(), "guide"))
	s.Equal("0", s.queryOne(`SELECT COUNT(*) FROM chunks_fts WHERE doc_id='guide'`))
	s.Equal("0", s.queryOne(`SELECT COUNT(*) FROM documents WHERE doc_id='guide'`))
}

func (s *RepositorySuite) TestPagesAndIndexTermsAreWritten() {
	s.document("manual", "Manual", "/library/manual.pdf", "abc")
	s.Require().NoError(s.repo.AddPagesAndTerms(
		s.T().Context(), "manual",
		map[int]string{2: "second page", 1: "first page"},
		[][2]string{{"dimmer", "4.1"}, {"patch", "4.2"}},
	))

	s.Equal("first page", s.queryOne(`SELECT text FROM pages WHERE doc_id='manual' AND page=1`))
	s.Equal("4.1", s.queryOne(`SELECT section FROM index_terms WHERE term='dimmer'`))
}

func (s *RepositorySuite) TestAPageWrittenTwiceKeepsTheLastText() {
	s.document("manual", "Manual", "/library/manual.pdf", "abc")
	ctx := s.T().Context()
	s.Require().NoError(s.repo.AddPagesAndTerms(ctx, "manual", map[int]string{1: "first"}, nil))
	s.Require().NoError(s.repo.AddPagesAndTerms(ctx, "manual", map[int]string{1: "corrected"}, nil))

	s.Equal("corrected", s.queryOne(`SELECT text FROM pages WHERE doc_id='manual' AND page=1`))
}

func (s *RepositorySuite) TestAJobIsCompletedWhenItsDocumentBecomesVisible() {
	// A document must never be searchable while its job still reads as
	// running, so both rows move in one transaction.
	res, err := s.db.Exec(
		`INSERT INTO ingest_jobs (source_path, status, created_at, updated_at)
		 VALUES ('/library/guide.md', 'running', datetime('now'), datetime('now'))`)
	s.Require().NoError(err)
	jobID, err := res.LastInsertId()
	s.Require().NoError(err)

	doc := ingest.Document{
		DocID: "guide", Title: "Guide", Format: "markdown",
		Identity: "/library/guide.md", Digest: "abc", Kind: domain.SourceKindFile,
	}
	s.Require().NoError(s.repo.Create(s.T().Context(), doc, ""))
	s.Require().NoError(s.repo.MarkReady(s.T().Context(), ingest.Ready{
		IngestedAt: stamped,
		JobID:      &jobID,
		DocID:      "guide",
		Warnings:   []byte(`{"quality":"degraded"}`),
		ChunkCount: 1,
	}))

	s.Equal("done", s.queryOne(`SELECT status FROM ingest_jobs WHERE id=?`, jobID))
	s.Equal("guide", s.queryOne(`SELECT doc_id FROM ingest_jobs WHERE id=?`, jobID))
	s.JSONEq(
		`{"quality":"degraded"}`,
		s.queryOne(`SELECT warnings FROM ingest_jobs WHERE id=?`, jobID),
	)
}

func (s *RepositorySuite) TestAFailedWriteLeavesNoPartialBatch() {
	// One batch is one transaction, so a chunk the schema refuses takes the
	// whole batch with it rather than leaving the document half indexed. The
	// second chunk here names a document that does not exist, which the
	// foreign key refuses.
	doc := ingest.Document{
		DocID: "guide", Title: "Guide", Format: "markdown",
		Identity: "/library/guide.md", Digest: "abc", Kind: domain.SourceKindFile,
	}
	s.Require().NoError(s.repo.Create(s.T().Context(), doc, ""))
	s.Require().NoError(s.repo.AddChunks(s.T().Context(), "absent", nil))

	err := s.repo.AddChunks(s.T().Context(), "absent", []domain.Chunk{chunk(0, "one")})
	s.Require().Error(err)
	s.Equal("0", s.queryOne(`SELECT COUNT(*) FROM chunks`))
}

func (s *RepositorySuite) TestAChunksColumnsRoundTrip() {
	s.document("manual", "Manual", "/library/manual.pdf", "abc")
	url, fragment, section := "https://example.com/docs/install", "packages", "4.1"
	pageStart, pageEnd, printed := 12, 13, 9
	s.Require().NoError(s.repo.AddChunks(s.T().Context(), "manual", []domain.Chunk{{
		Section:          &section,
		PageStart:        &pageStart,
		PageEnd:          &pageEnd,
		PrintedPageStart: &printed,
		URL:              &url,
		Fragment:         &fragment,
		HeadingPath:      "Manual > Install > Packages",
		Text:             "Packages are published for every release.",
		Ordinal:          1,
		ImageCount:       2,
		Kind:             domain.KindKeywordReference,
	}}))

	var got struct {
		section, url, fragment, kind string
		pageStart, pageEnd, printed  int
		images                       int
	}
	err := s.db.QueryRow(
		`SELECT section, url, fragment, kind, page_start, page_end, printed_page_start,
		        image_count
		   FROM chunks WHERE doc_id='manual' AND ordinal=1`,
	).Scan(&got.section, &got.url, &got.fragment, &got.kind,
		&got.pageStart, &got.pageEnd, &got.printed, &got.images)
	s.Require().NoError(err)
	s.Equal(section, got.section)
	s.Equal(url, got.url)
	s.Equal(fragment, got.fragment)
	s.Equal("keyword-reference", got.kind, "the text v1 already stores")
	s.Equal(pageStart, got.pageStart)
	s.Equal(pageEnd, got.pageEnd)
	s.Equal(printed, got.printed)
	s.Equal(2, got.images)
}

// TestTheServiceRunsAgainstThisRepository wires the real service to the real
// database, since a port satisfied in the type system can still disagree with
// its caller about what a method means.
func (s *RepositorySuite) TestTheServiceRunsAgainstThisRepository() {
	service := ingest.New(s.repo, func() time.Time { return stamped })
	result, err := service.Run(s.T().Context(), &staticSource{}, ingest.Options{})
	s.Require().NoError(err)
	s.Equal(ingest.Ingested, result.Outcome)
	s.Equal("operator-guide", result.DocID)
	s.Equal("ready", s.queryOne(`SELECT status FROM documents WHERE doc_id='operator-guide'`))
	s.Positive(result.ChunkCount)
}

// staticSource is a document already in memory, so the wiring test exercises
// the repository rather than a file or a crawl.
type staticSource struct{}

func (*staticSource) Kind() domain.SourceKind { return domain.SourceKindFile }
func (*staticSource) Identity() string        { return "/library/guide.md" }
func (*staticSource) Digest() string          { return "abc123" }

func (*staticSource) Acquire(context.Context, ingest.Progress) error { return nil }

func (*staticSource) Extract(
	context.Context,
	ingest.Progress,
) (*domain.Extraction, error) {
	blocks := []domain.Block{
		domain.NewOffsetBlock(
			[]string{"Install"}, 0,
			"Unpack the archive and run the installer from a terminal.",
		),
		domain.NewOffsetBlock(
			[]string{"Usage"}, 60,
			"Point the tool at a description file and read the result.",
		),
	}
	return domain.NewExtraction(
		"Operator Guide", "markdown", domain.SourceATXHeadings, blocks), nil
}
