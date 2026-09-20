package domain_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

// MeasureSuite holds the measurements to what verify_document computes for
// the same chunks.
//
// One divergence, deliberate: Python names the largest and smallest chunks
// by their database id, and a domain function has no id to name them by,
// since the database assigns it. The ordinal is used instead, which answers
// the same question better: a reader can find ordinal 3 in the document.
type MeasureSuite struct{ suite.Suite }

func TestMeasure(t *testing.T) { suite.Run(t, new(MeasureSuite)) }

// manual is the document Python was run against, a paginated one with a
// missing page, a gap in its ordinals and a scattered section.
func manualChunks() []domain.Chunk {
	chunk := func(ordinal int, section string, start, end int, path, text string, images int) domain.Chunk {
		s, ps, pe := section, start, end
		return domain.Chunk{
			Section:     &s,
			PageStart:   &ps,
			PageEnd:     &pe,
			HeadingPath: path,
			Text:        text,
			Ordinal:     ordinal,
			ImageCount:  images,
		}
	}
	return []domain.Chunk{
		chunk(0, "1", 1, 2, "M > A", strings.Repeat("Alpha text here. ", 40), 0),
		chunk(1, "1.1", 3, 3, "M > A > B", strings.Repeat("Beta text. ", 5), 2),
		chunk(2, "2", 5, 5, "M > C", strings.Repeat("Gamma text. ", 80), 0),
		// Ordinal 3 is missing, and section 2 is split across the gap.
		chunk(4, "2", 6, 6, "M > C", "Delta. ", 0),
	}
}

func pages(n int) *int { return &n }

func (s *MeasureSuite) TestTheSpreadMatchesPython() {
	m := domain.Measure(manualChunks(), pages(6))

	s.Equal(2, m.Tokens.Min)
	s.Equal(208, m.Tokens.Median)
	s.Equal(312, m.Tokens.P95)
	s.Equal(312, m.Tokens.Max)
	s.Equal(135, m.Tokens.Mean)
	s.Equal(541, m.Tokens.Total)
}

func (s *MeasureSuite) TestAPageNoChunkClaimsIsNamed() {
	// Page 4 fell between two chunks: something was extracted and never
	// stored, and no search will ever return it.
	s.Equal([]int{4}, domain.Measure(manualChunks(), pages(6)).UncoveredPages)
}

func (s *MeasureSuite) TestAGapInTheNumberingIsNamed() {
	// A gap means a batch was lost between transactions, which every read
	// path would step over silently.
	s.Equal([]int{3}, domain.Measure(manualChunks(), pages(6)).OrdinalGaps)
}

func (s *MeasureSuite) TestASectionSplitAcrossTheDocumentIsNamed() {
	// A boundary misfired and scattered one section key, which silently
	// breaks the index_terms join.
	s.Equal([]string{"2"}, domain.Measure(manualChunks(), pages(6)).ScatteredSections)
}

func (s *MeasureSuite) TestTheExtremesAreNamedBiggestFirst() {
	m := domain.Measure(manualChunks(), pages(6))

	s.Require().Len(m.Longest, 4)
	s.Equal(2, m.Longest[0].Ordinal, "the 312-token chunk")
	s.Equal(312, m.Longest[0].Tokens)
	s.Equal("M > C", m.Longest[0].HeadingPath)

	s.Require().Len(m.Shortest, 4)
	s.Equal(4, m.Shortest[0].Ordinal, "the 2-token chunk")
	s.Equal(2, m.Shortest[0].Tokens)
}

func (s *MeasureSuite) TestImagesAreCounted() {
	s.Equal(1, domain.Measure(manualChunks(), pages(6)).ChunksWithImages)
}

func (s *MeasureSuite) TestAChunkWithNoPageIsMissingItsLocator() {
	// Only for a paginated document: a markdown chunk names no page because
	// there are none, which is not a defect.
	chunks := []domain.Chunk{{HeadingPath: "M", Text: "Some text.", Ordinal: 0}}

	s.Equal(1, domain.Measure(chunks, pages(3)).MissingLocator)
	s.Equal(0, domain.Measure(chunks, nil).MissingLocator)
}

func (s *MeasureSuite) TestAnUnpaginatedDocumentHasNoUncoveredPages() {
	s.Empty(domain.Measure(manualChunks(), nil).UncoveredPages)
}

func (s *MeasureSuite) TestADocumentWithNoChunksMeasuresEmpty() {
	m := domain.Measure(nil, pages(3))

	s.Equal(0, m.ChunkCount)
	s.Empty(m.Longest)
	s.Empty(m.UncoveredPages, "nothing to compare the pages against")
	s.Equal(0, m.Tokens.Total)
}

func (s *MeasureSuite) TestTheExtremesAreCappedAndStable() {
	var chunks []domain.Chunk
	for i := range 30 {
		chunks = append(chunks, domain.Chunk{
			HeadingPath: "M", Text: strings.Repeat("word ", i+1), Ordinal: i,
		})
	}
	m := domain.Measure(chunks, nil)

	s.Len(m.Longest, 10)
	s.Len(m.Shortest, 10)
	s.Equal(29, m.Longest[0].Ordinal)
	s.Equal(0, m.Shortest[0].Ordinal)
}
