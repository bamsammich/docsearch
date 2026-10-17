package domain_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

// GradeSuite holds the grader to what docsearch.verify.grade produces, on
// the distributions the Python tests exercise.
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

// golden is one distribution and what Python made of it.
type golden struct {
	Chunks   []goldenChunk   `json:"chunks"`
	Findings []goldenFinding `json:"findings"`
}

func (s *GradeSuite) TestEveryDistributionGradesAsPythonGradesIt() {
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

			chunks := make([]domain.ChunkFacts, len(want.Chunks))
			for i, c := range want.Chunks {
				chunks[i] = domain.ChunkFacts{
					Text:        c.Text,
					HeadingPath: c.HeadingPath,
					Tokens:      c.Tokens,
					ImageCount:  c.ImageCount,
					Depth:       c.Depth,
					Numbered:    c.Numbered,
				}
			}

			got := domain.Grade(chunks)
			s.Require().Len(got, len(want.Findings))
			for i, finding := range got {
				s.Equal(want.Findings[i].Code, finding.Code)
				s.Equal(want.Findings[i].Severity, finding.Severity.String())
				s.Equal(want.Findings[i].Detail, finding.Detail)
			}
		})
	}
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
