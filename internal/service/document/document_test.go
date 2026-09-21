package document_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/service/document/mocks"
)

// DocumentSuite covers what verification reports, and which of its two
// questions each answer belongs to.
type DocumentSuite struct {
	suite.Suite
	repo      *mocks.MockRepository
	inspector *mocks.MockInspector
}

func TestDocument(t *testing.T) { suite.Run(t, new(DocumentSuite)) }

func (s *DocumentSuite) SetupTest() {
	s.repo = mocks.NewMockRepository(s.T())
	s.inspector = mocks.NewMockInspector(s.T())
}

func (s *DocumentSuite) service() *document.Service {
	return document.New(s.repo, s.inspector)
}

// holds is a document the index has, with the chunks given.
func (s *DocumentSuite) holds(doc document.Document, chunks []domain.Chunk) {
	s.repo.EXPECT().Get(mock.Anything, doc.DocID).Return(&doc, nil)
	s.repo.EXPECT().Chunks(mock.Anything, doc.DocID).Return(chunks, nil).Maybe()
	s.repo.EXPECT().IndexTermSections(mock.Anything, doc.DocID).Return(nil, nil).Maybe()
	s.repo.EXPECT().IndexTermCount(mock.Anything, doc.DocID).Return(0, nil).Maybe()
}

// prose is one well-formed chunk.
func prose(ordinal int, section, path string) domain.Chunk {
	s := section
	return domain.Chunk{
		Section:     &s,
		HeadingPath: path,
		Text:        strings.Repeat("The console stores each cue in a sequence. ", 20),
		Ordinal:     ordinal,
	}
}

// pages returns a page count.
func pages(n int) *int { return &n }

func (s *DocumentSuite) TestASoundDocumentReportsNoProblems() {
	chunks := []domain.Chunk{
		prose(0, "1", "Manual > Install"),
		prose(1, "2", "Manual > Usage"),
	}
	s.holds(document.Document{DocID: "guide", Title: "Guide", Status: "ready"}, chunks)

	report, err := s.service().Verify(s.T().Context(), "guide")
	s.Require().NoError(err)
	s.Empty(report.Problems)
	s.Equal(domain.VerdictGood, report.Verdict)
	s.Equal(2, report.Measurements.ChunkCount)
}

func (s *DocumentSuite) TestAGapInTheOrdinalsIsAnIntegrityProblem() {
	// A batch was lost between transactions, which every read path would
	// step over silently.
	chunks := []domain.Chunk{
		prose(0, "1", "Manual > Install"),
		prose(2, "2", "Manual > Usage"),
	}
	s.holds(document.Document{DocID: "guide", Status: "ready"}, chunks)

	report, err := s.service().Verify(s.T().Context(), "guide")
	s.Require().NoError(err)
	s.Require().Len(report.Problems, 1)
	s.Contains(report.Problems[0], "ordinals skip")
}

func (s *DocumentSuite) TestAPageNoChunkClaimsIsAnIntegrityProblem() {
	first, second := 1, 3
	chunks := []domain.Chunk{
		{PageStart: &first, PageEnd: &first, HeadingPath: "M > A", Text: "Alpha.", Ordinal: 0},
		{PageStart: &second, PageEnd: &second, HeadingPath: "M > B", Text: "Beta.", Ordinal: 1},
	}
	s.holds(document.Document{DocID: "manual", Status: "ready", PageCount: pages(3)}, chunks)

	report, err := s.service().Verify(s.T().Context(), "manual")
	s.Require().NoError(err)
	s.Contains(strings.Join(report.Problems, "\n"), "claimed by no chunk")
}

func (s *DocumentSuite) TestAnIndexTermPointingAtNothingIsAnIntegrityProblem() {
	// Matched as a subtree: an entry pointing at chapter 4 refers to the
	// whole chapter, so only a section with nothing beneath it is unjoinable.
	chunks := []domain.Chunk{prose(0, "1", "Manual > Install")}
	doc := document.Document{DocID: "manual", Status: "ready"}
	s.repo.EXPECT().Get(mock.Anything, "manual").Return(&doc, nil)
	s.repo.EXPECT().Chunks(mock.Anything, "manual").Return(chunks, nil)
	s.repo.EXPECT().IndexTermSections(mock.Anything, "manual").Return([]string{"1", "9"}, nil)
	s.repo.EXPECT().IndexTermCount(mock.Anything, "manual").Return(14, nil)
	s.repo.EXPECT().SectionHasChunks(mock.Anything, "manual", "1").Return(true, nil)
	s.repo.EXPECT().SectionHasChunks(mock.Anything, "manual", "9").Return(false, nil)

	report, err := s.service().Verify(s.T().Context(), "manual")
	s.Require().NoError(err)
	s.Require().Len(report.Problems, 1)
	s.Contains(report.Problems[0], "[9]")
	s.Contains(report.Problems[0], "resolve to nothing")
	s.Equal([]string{"9"}, report.UnjoinableSections)
	s.Equal(14, report.IndexTerms, "entries, not the sections they point at")
}

func (s *DocumentSuite) TestQualityAndIntegrityAreReportedApart() {
	// A document whose rows are perfectly consistent can still be shaped so
	// retrieval cannot work on it, and a report that conflated the two would
	// call it fine.
	var chunks []domain.Chunk
	for i := range 40 {
		chunks = append(chunks, domain.Chunk{
			HeadingPath: "Manual",
			Text:        strings.Repeat("word ", 2000),
			Ordinal:     i,
		})
	}
	s.holds(document.Document{DocID: "sliced", Status: "ready"}, chunks)

	report, err := s.service().Verify(s.T().Context(), "sliced")
	s.Require().NoError(err)
	s.Empty(report.Problems, "the rows are consistent")
	s.Equal(domain.VerdictUnusable, report.Verdict, "and the chunks are unusable")
}

func (s *DocumentSuite) TestADocumentWithNoChunksIsAProblemRatherThanAGrade() {
	s.holds(document.Document{DocID: "empty", Status: "ready"}, nil)

	report, err := s.service().Verify(s.T().Context(), "empty")
	s.Require().NoError(err)
	s.Equal([]string{"document has no chunks"}, report.Problems)
	s.Empty(report.Findings, "there is nothing to grade")
}

func (s *DocumentSuite) TestADocumentThatIsNotThereSaysSo() {
	missing := errors.New("not found")
	s.repo.EXPECT().Get(mock.Anything, "absent").Return(nil, missing)

	_, err := s.service().Verify(s.T().Context(), "absent")
	s.Require().ErrorIs(err, missing)
}

func (s *DocumentSuite) TestRemovingADocumentChecksItExistsFirst() {
	// Deleting nothing and reporting success is how a caller comes to
	// believe a document is gone when it is not.
	missing := errors.New("not found")
	s.repo.EXPECT().Get(mock.Anything, "absent").Return(nil, missing)

	s.Require().ErrorIs(s.service().Remove(s.T().Context(), "absent"), missing)
}

func (s *DocumentSuite) TestRemovingADocumentDeletesIt() {
	doc := document.Document{DocID: "guide"}
	s.repo.EXPECT().Get(mock.Anything, "guide").Return(&doc, nil)
	s.repo.EXPECT().Delete(mock.Anything, "guide").Return(nil)

	s.Require().NoError(s.service().Remove(s.T().Context(), "guide"))
}

func (s *DocumentSuite) TestInspectionIsPassedThrough() {
	want := &domain.InspectReport{Target: "/library/guide.md", Format: "md"}
	s.inspector.EXPECT().Inspect(mock.Anything, "/library/guide.md").Return(want, nil)

	got, err := s.service().Inspect(s.T().Context(), "/library/guide.md")
	s.Require().NoError(err)
	s.Equal(want, got)
}
