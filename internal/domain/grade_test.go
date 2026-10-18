package domain_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

// GradeSuite holds the grader to the distributions the Python tests
// exercised. Each golden began as what docsearch.verify.grade produced; the
// unaddressable details in budget-sliced and headless were rewritten when
// long sections under markup headings stopped counting as a defect.
//
// Detail strings are compared too. The detail is what an operator reads, so
// a divergence in a percentage or a thousands separator is a divergence in
// the report, and comparing only the codes would miss it.
type GradeSuite struct{ suite.Suite }

func TestGrade(t *testing.T) { suite.Run(t, new(GradeSuite)) }

// goldenChunk is one chunk as Python's ChunkStat serializes.
type goldenChunk struct {
	Text        string `json:"text"`
	HeadingPath string `json:"heading_path"`
	Tokens      int    `json:"tokens"`
	ImageCount  int    `json:"image_count"`
	Depth       int    `json:"depth"`
	Numbered    bool   `json:"numbered"`
}

// goldenFinding is one finding as Python reports it. The severity is its
// text, since that is what the report carries.
type goldenFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
}

// golden is one distribution and what grading makes of it. The structure
// source is absent from every Python golden, which graded as though the
// source could have missed headings.
type golden struct {
	StructureSource string          `json:"structure_source"`
	Chunks          []goldenChunk   `json:"chunks"`
	Findings        []goldenFinding `json:"findings"`
}

// facts are the golden's chunks as the grader reads them.
func (g golden) facts() []domain.ChunkFacts {
	chunks := make([]domain.ChunkFacts, len(g.Chunks))
	for i, c := range g.Chunks {
		chunks[i] = domain.ChunkFacts{
			Text:        c.Text,
			HeadingPath: c.HeadingPath,
			Tokens:      c.Tokens,
			ImageCount:  c.ImageCount,
			Depth:       c.Depth,
			Numbered:    c.Numbered,
		}
	}
	return chunks
}

// sourceOf is the structure source a golden names, SourceUnknown where it
// names none.
func (s *GradeSuite) sourceOf(g golden) domain.StructureSource {
	if g.StructureSource == "" {
		return domain.SourceUnknown
	}
	source, err := domain.ParseStructureSource(g.StructureSource)
	s.Require().NoError(err)
	return source
}

func (s *GradeSuite) TestEveryDistributionGradesAsItsGoldenSays() {
	dir := filepath.Join("..", "..", "testdata", "grading")
	entries, err := os.ReadDir(dir)
	s.Require().NoError(err)
	s.Require().NotEmpty(entries)

	for _, entry := range entries {
		s.Run(entry.Name(), func() {
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			s.Require().NoError(err)
			var want golden
			s.Require().NoError(json.Unmarshal(raw, &want))

			got := domain.Grade(want.facts(), s.sourceOf(want))
			s.Require().Len(got, len(want.Findings))
			for i, finding := range got {
				s.Equal(want.Findings[i].Code, finding.Code)
				s.Equal(want.Findings[i].Severity, finding.Severity.String())
				s.Equal(want.Findings[i].Detail, finding.Detail)
			}
		})
	}
}

// paperFacts is a paper whose every heading is a path: twenty sections, two
// the token budget split five ways and a reference list of twelve.
func paperFacts() []domain.ChunkFacts {
	return factsIn([]int{2, 1, 1, 1, 5, 2, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1, 5, 2, 2, 12})
}

// factsIn is a document with the given number of well-sized chunks under
// each of its headings in turn.
func factsIn(counts []int) []domain.ChunkFacts {
	var facts []domain.ChunkFacts
	for i, n := range counts {
		for range n {
			facts = append(facts, domain.ChunkFacts{
				HeadingPath: fmt.Sprintf("Paper > Section %d", i+1),
				Tokens:      600,
				Depth:       2,
			})
		}
	}
	return facts
}

func codesOf(findings []domain.Finding) []string {
	codes := make([]string, len(findings))
	for i, f := range findings {
		codes[i] = f.Code
	}
	return codes
}

// Few paths per chunk mean missed headings only where a source could miss
// them. Under markup headings the paper's long sections are no defect.
func (s *GradeSuite) TestLongSectionsAreUnaddressableOnlyUnderHeadingsThatCanMissSome() {
	tests := []struct {
		want   []string
		source domain.StructureSource
	}{
		{source: domain.SourceATXHeadings, want: []string{}},
		{source: domain.SourceHTMLNesting, want: []string{}},
		{source: domain.SourceHeadingStyles, want: []string{}},
		{source: domain.SourceFontHeuristic, want: []string{"unaddressable"}},
		{source: domain.SourceOutline, want: []string{"unaddressable"}},
		{source: domain.SourceUnknown, want: []string{"unaddressable"}},
	}
	for _, tt := range tests {
		s.Run(tt.source.String(), func() {
			s.Equal(tt.want, codesOf(domain.Grade(paperFacts(), tt.source)))
		})
	}
}

func (s *GradeSuite) TestTheFindingNamesTheSourceThatCouldMissHeadings() {
	findings := domain.Grade(paperFacts(), domain.SourceFontHeuristic)
	s.Require().Len(findings, 1)
	s.Equal(domain.VerdictDegraded, findings[0].Severity)
	s.Contains(
		findings[0].Detail,
		"44 chunks share only 20 distinct heading paths (0.45 per chunk)",
	)
	s.Contains(findings[0].Detail, "under headings from structure source `font_heuristic`")
}

// A document under one heading path is not divided by its markup either.
func (s *GradeSuite) TestAMarkupDocumentUnderOneHeadingIsUnaddressable() {
	findings := domain.Grade(factsIn([]int{30}), domain.SourceATXHeadings)
	s.Require().Len(findings, 1)
	s.Equal("unaddressable", findings[0].Code)
	s.Equal(domain.VerdictUnusable, findings[0].Severity, "one path for 30 chunks")
	s.Contains(
		findings[0].Detail,
		"30 of 30 chunks (100%) share one heading path, 'Paper > Section 1'",
	)
}

func (s *GradeSuite) TestOneSectionHoldingMostOfAMarkupDocumentIsUnaddressable() {
	findings := domain.Grade(factsIn([]int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 18}),
		domain.SourceATXHeadings)
	s.Require().Len(findings, 1)
	s.Equal(domain.VerdictDegraded, findings[0].Severity, "13 paths for 30 chunks")
	s.Contains(findings[0].Detail, "18 of 30 chunks (60%) share one heading path")
}

// Of two paths holding as many chunks, the finding names the first in
// document order, even when that section's chunks are scattered.
func (s *GradeSuite) TestTheUndividedPathIsTheFirstOfEqualsInDocumentOrder() {
	facts := factsIn([]int{15, 15})
	facts = append(facts[:1], append(facts[15:], facts[1:15]...)...)
	findings := domain.Grade(facts, domain.SourceATXHeadings)
	s.Require().Len(findings, 1)
	s.Contains(
		findings[0].Detail,
		"15 of 30 chunks (50%) share one heading path, 'Paper > Section 1'",
	)
}

func (s *GradeSuite) TestTheVerdictIsTheWorstFinding() {
	s.Equal(domain.VerdictGood, domain.GradeVerdict(nil))
	s.Equal(domain.VerdictDegraded, domain.GradeVerdict([]domain.Finding{
		{Code: "boilerplate", Severity: domain.VerdictDegraded},
	}))
	s.Equal(domain.VerdictUnusable, domain.GradeVerdict([]domain.Finding{
		{Code: "boilerplate", Severity: domain.VerdictDegraded},
		{Code: "oversized", Severity: domain.VerdictUnusable},
	}))
}

func (s *GradeSuite) TestFactsComeOffAChunk() {
	section := "4.1"
	facts := domain.FactsOf(domain.Chunk{
		Section:     &section,
		HeadingPath: "Manual > Install > Packages",
		Text:        "Packages are published for every release.",
		ImageCount:  2,
	})
	s.Equal(3, facts.Depth)
	s.True(facts.Numbered)
	s.Positive(facts.Tokens)
	s.True(facts.FigureDominated(), "an image and almost no text")
}

func (s *GradeSuite) TestAnUnnumberedChunkIsMergeable() {
	facts := domain.FactsOf(domain.Chunk{HeadingPath: "Manual", Text: "Short."})
	s.False(facts.Numbered)
	s.Equal(1, facts.Depth)
}
