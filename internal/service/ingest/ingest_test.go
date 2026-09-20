package ingest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/ingest/mocks"
)

// stamped is the clock every ingest here is stamped from, so a persisted
// timestamp is something a test can state.
var stamped = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// IngestSuite covers what one ingest writes, what it refuses, and what it
// leaves behind when it fails part way through.
type IngestSuite struct {
	suite.Suite
	repo   *mocks.MockRepository
	source *mocks.MockSource
}

func TestIngest(t *testing.T) { suite.Run(t, new(IngestSuite)) }

func (s *IngestSuite) SetupTest() {
	s.repo = mocks.NewMockRepository(s.T())
	s.source = mocks.NewMockSource(s.T())
}

func (s *IngestSuite) service() *ingest.Service {
	return ingest.New(s.repo, func() time.Time { return stamped })
}

// acquires is a source that reads cleanly and hashes to digest.
func (s *IngestSuite) acquires(digest, identity string) {
	s.source.EXPECT().Acquire(mock.Anything, mock.Anything).Return(nil).Maybe()
	s.source.EXPECT().Digest().Return(digest).Maybe()
	s.source.EXPECT().Identity().Return(identity).Maybe()
	s.source.EXPECT().Kind().Return(domain.SourceKindFile).Maybe()
}

// extracts is a source whose extraction holds one heading and two paragraphs,
// which chunks to something the structure policy is content with.
func (s *IngestSuite) extracts(title string) {
	s.source.EXPECT().
		Extract(mock.Anything, mock.Anything).
		Return(manual(title), nil).
		Maybe()
}

// manual is an extraction of a short document with real structure.
func manual(title string) *domain.Extraction {
	blocks := []domain.Block{
		block([]string{"Install"}, 0, "Unpack the archive and run the installer from a terminal."),
		block(
			[]string{"Install", "Linux"},
			60,
			"Add the repository and install the package by name.",
		),
		block([]string{"Usage"}, 120, "Point the tool at a description file and read the result."),
	}
	return domain.NewExtraction(title, "markdown", domain.SourceATXHeadings, blocks)
}

func block(path []string, offset int, text string) domain.Block {
	return domain.NewOffsetBlock(path, offset, text)
}

func (s *IngestSuite) TestADocumentIsWrittenThenMadeVisible() {
	s.acquires("abc123", "/library/guide.md")
	s.extracts("Operator Guide")
	s.repo.EXPECT().ReadyWithDigest(mock.Anything, "abc123").Return(nil, nil)
	s.repo.EXPECT().DocIDForIdentity(mock.Anything, "/library/guide.md").Return("", nil)
	s.repo.EXPECT().DocIDsWithPrefix(mock.Anything, "operator-guide").Return(nil, nil)

	var created ingest.Document
	s.repo.EXPECT().Create(mock.Anything, mock.Anything, "").
		Run(func(_ context.Context, doc ingest.Document, _ string) { created = doc }).
		Return(nil)
	s.repo.EXPECT().AddChunks(mock.Anything, "operator-guide", mock.Anything).Return(nil)
	s.repo.EXPECT().
		AddPagesAndTerms(mock.Anything, "operator-guide", mock.Anything, mock.Anything).
		Return(nil)

	var published ingest.Ready
	s.repo.EXPECT().MarkReady(mock.Anything, mock.Anything).
		Run(func(_ context.Context, ready ingest.Ready) { published = ready }).
		Return(nil)

	result, err := s.service().Run(s.T().Context(), s.source, ingest.Options{})
	s.Require().NoError(err)
	s.Equal(ingest.Ingested, result.Outcome)
	s.Equal("operator-guide", result.DocID)
	s.Equal("Operator Guide", created.Title)
	s.Equal("/library/guide.md", created.Identity)
	s.Equal(domain.SourceKindFile, created.Kind)
	s.Equal(stamped, published.IngestedAt)
	s.Equal(result.ChunkCount, published.ChunkCount)
	s.Contains(string(published.Warnings), `"quality":"ok"`)
}

func (s *IngestSuite) TestBytesAlreadyIndexedAreNotIngestedAgain() {
	s.acquires("abc123", "/library/copy.md")
	s.repo.EXPECT().ReadyWithDigest(mock.Anything, "abc123").
		Return(&ingest.Existing{DocID: "operator-guide", Title: "Operator Guide", ChunkCount: 7}, nil)

	result, err := s.service().Run(s.T().Context(), s.source, ingest.Options{})
	s.Require().NoError(err)
	s.Equal(ingest.Unchanged, result.Outcome)
	s.Equal(7, result.ChunkCount)
	s.Contains(result.Note, "already ingested as 'operator-guide'")
}

func (s *IngestSuite) TestARetitledSourceReplacesItsOwnRows() {
	// Keyed on identity rather than on the title's slug: slugifying a new
	// title would file the document twice and orphan the first set of rows.
	s.acquires("def456", "/library/guide.md")
	s.extracts("Operator Handbook")
	s.repo.EXPECT().ReadyWithDigest(mock.Anything, "def456").Return(nil, nil)
	s.repo.EXPECT().DocIDForIdentity(mock.Anything, "/library/guide.md").
		Return("operator-guide", nil)
	s.repo.EXPECT().Create(mock.Anything, mock.Anything, "operator-guide").Return(nil)
	s.repo.EXPECT().AddChunks(mock.Anything, "operator-guide", mock.Anything).Return(nil)
	s.repo.EXPECT().AddPagesAndTerms(mock.Anything, "operator-guide", mock.Anything, mock.Anything).
		Return(nil)
	s.repo.EXPECT().MarkReady(mock.Anything, mock.Anything).Return(nil)

	result, err := s.service().Run(s.T().Context(), s.source, ingest.Options{})
	s.Require().NoError(err)
	s.Equal(ingest.Replaced, result.Outcome)
	s.Equal("operator-guide", result.DocID, "the identifier it already had")
}

func (s *IngestSuite) TestATakenIdentifierIsNumberedPast() {
	s.acquires("def456", "/library/second-guide.md")
	s.extracts("Operator Guide")
	s.repo.EXPECT().ReadyWithDigest(mock.Anything, mock.Anything).Return(nil, nil)
	s.repo.EXPECT().DocIDForIdentity(mock.Anything, mock.Anything).Return("", nil)
	s.repo.EXPECT().DocIDsWithPrefix(mock.Anything, "operator-guide").
		Return([]string{"operator-guide", "operator-guide-2"}, nil)
	s.repo.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	s.repo.EXPECT().AddChunks(mock.Anything, "operator-guide-3", mock.Anything).Return(nil)
	s.repo.EXPECT().AddPagesAndTerms(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil)
	s.repo.EXPECT().MarkReady(mock.Anything, mock.Anything).Return(nil)

	result, err := s.service().Run(s.T().Context(), s.source, ingest.Options{})
	s.Require().NoError(err)
	s.Equal("operator-guide-3", result.DocID)
}

func (s *IngestSuite) TestAnExtractionWithNoTextIsRefusedRatherThanIndexed() {
	// Such a document would hold an identifier, report as ready, and never be
	// returned by any query.
	s.acquires("ghi789", "/library/empty.md")
	s.source.EXPECT().Extract(mock.Anything, mock.Anything).
		Return(domain.NewExtraction("Empty", "markdown", domain.SourceATXHeadings, nil), nil)
	s.repo.EXPECT().ReadyWithDigest(mock.Anything, mock.Anything).Return(nil, nil)
	s.repo.EXPECT().DocIDForIdentity(mock.Anything, mock.Anything).Return("", nil)
	s.repo.EXPECT().DocIDsWithPrefix(mock.Anything, mock.Anything).Return(nil, nil)

	_, err := s.service().Run(s.T().Context(), s.source, ingest.Options{})

	var refused *ingest.StructureError
	s.Require().ErrorAs(err, &refused)
	s.Contains(refused.Error(), "produced no chunks")
	s.Contains(refused.Error(), "/library/empty.md")
}

func (s *IngestSuite) TestAFailureAfterTheFirstRowLeavesNothingBehind() {
	s.acquires("jkl012", "/library/guide.md")
	s.extracts("Operator Guide")
	s.repo.EXPECT().ReadyWithDigest(mock.Anything, mock.Anything).Return(nil, nil)
	s.repo.EXPECT().DocIDForIdentity(mock.Anything, mock.Anything).Return("", nil)
	s.repo.EXPECT().DocIDsWithPrefix(mock.Anything, mock.Anything).Return(nil, nil)
	s.repo.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	s.repo.EXPECT().AddChunks(mock.Anything, mock.Anything, mock.Anything).
		Return(errors.New("disk full"))

	deleted := ""
	s.repo.EXPECT().Delete(mock.Anything, mock.Anything).
		Run(func(_ context.Context, docID string) { deleted = docID }).
		Return(nil)

	_, err := s.service().Run(s.T().Context(), s.source, ingest.Options{})
	s.Require().ErrorContains(err, "disk full")
	s.Equal("operator-guide", deleted, "the half-written document is removed")
}

func (s *IngestSuite) TestACancelledIngestStopsBeforeWriting() {
	ctx, cancel := context.WithCancel(s.T().Context())
	s.source.EXPECT().Acquire(mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ ingest.Progress) { cancel() }).
		Return(nil)
	s.source.EXPECT().Digest().Return("mno345").Maybe()

	_, err := s.service().Run(ctx, s.source, ingest.Options{})
	s.Require().ErrorIs(err, ingest.ErrCancelled)
}

func (s *IngestSuite) TestTheIdentifierIsReportedBeforeAnyRowIsWritten() {
	// A worker records the identifier against its job while the ingest runs,
	// so a job that dies mid-write still says which document it was writing.
	s.acquires("pqr678", "/library/guide.md")
	s.extracts("Operator Guide")
	s.repo.EXPECT().ReadyWithDigest(mock.Anything, mock.Anything).Return(nil, nil)
	s.repo.EXPECT().DocIDForIdentity(mock.Anything, mock.Anything).Return("", nil)
	s.repo.EXPECT().DocIDsWithPrefix(mock.Anything, mock.Anything).Return(nil, nil)

	announced := ""
	s.repo.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ ingest.Document, _ string) {
			s.Equal("operator-guide", announced, "announced before the first row")
		}).
		Return(nil)
	s.repo.EXPECT().AddChunks(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	s.repo.EXPECT().AddPagesAndTerms(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil)
	s.repo.EXPECT().MarkReady(mock.Anything, mock.Anything).Return(nil)

	_, err := s.service().Run(s.T().Context(), s.source, ingest.Options{
		OnDocID: func(docID string) { announced = docID },
	})
	s.Require().NoError(err)
}

func (s *IngestSuite) TestAJobIsCompletedInTheTransactionThatPublishes() {
	// A document must never be searchable while its job still reads as
	// running, so the job travels with the row that makes it visible.
	jobID := int64(42)
	s.acquires("stu901", "/library/guide.md")
	s.extracts("Operator Guide")
	s.repo.EXPECT().ReadyWithDigest(mock.Anything, mock.Anything).Return(nil, nil)
	s.repo.EXPECT().DocIDForIdentity(mock.Anything, mock.Anything).Return("", nil)
	s.repo.EXPECT().DocIDsWithPrefix(mock.Anything, mock.Anything).Return(nil, nil)
	s.repo.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	s.repo.EXPECT().AddChunks(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	s.repo.EXPECT().AddPagesAndTerms(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil)

	var published ingest.Ready
	s.repo.EXPECT().MarkReady(mock.Anything, mock.Anything).
		Run(func(_ context.Context, ready ingest.Ready) { published = ready }).
		Return(nil)

	_, err := s.service().Run(s.T().Context(), s.source, ingest.Options{JobID: &jobID})
	s.Require().NoError(err)
	s.Require().NotNil(published.JobID)
	s.Equal(jobID, *published.JobID)
}

func (s *IngestSuite) TestProgressIsReportedThroughIndexing() {
	s.acquires("vwx234", "/library/guide.md")
	s.extracts("Operator Guide")
	s.repo.EXPECT().ReadyWithDigest(mock.Anything, mock.Anything).Return(nil, nil)
	s.repo.EXPECT().DocIDForIdentity(mock.Anything, mock.Anything).Return("", nil)
	s.repo.EXPECT().DocIDsWithPrefix(mock.Anything, mock.Anything).Return(nil, nil)
	s.repo.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	s.repo.EXPECT().AddChunks(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	s.repo.EXPECT().AddPagesAndTerms(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil)
	s.repo.EXPECT().MarkReady(mock.Anything, mock.Anything).Return(nil)

	var phases []ingest.Phase
	_, err := s.service().Run(s.T().Context(), s.source, ingest.Options{
		Progress: func(phase ingest.Phase, _, _ int) { phases = append(phases, phase) },
	})
	s.Require().NoError(err)
	s.Equal([]ingest.Phase{ingest.PhaseChunk, ingest.PhaseIndex}, phases)
}
