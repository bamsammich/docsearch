package document_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/document"
)

// goldenChunk is one extreme chunk of a golden report.
type goldenChunk struct {
	Path    string `json:"path"`
	Ordinal int    `json:"ordinal"`
	Tokens  int    `json:"tokens"`
}

// goldenFields is the Python dataclass as it was written out.
type goldenFields struct {
	PageCount               *int             `json:"page_count"`
	DocID                   string           `json:"doc_id"`
	Title                   string           `json:"title"`
	Format                  string           `json:"format"`
	Status                  string           `json:"status"`
	UncoveredPages          []int            `json:"uncovered_pages"`
	Longest                 []goldenChunk    `json:"longest"`
	Shortest                []goldenChunk    `json:"shortest"`
	UnjoinableIndexSections []string         `json:"unjoinable_index_sections"`
	Problems                []string         `json:"problems"`
	Findings                []domain.Finding `json:"findings"`
	ChunkCount              int              `json:"chunk_count"`
	TokenMin                int              `json:"token_min"`
	TokenMedian             int              `json:"token_median"`
	TokenP95                int              `json:"token_p95"`
	TokenMax                int              `json:"token_max"`
	TokenMean               int              `json:"token_mean"`
	TotalTokens             int              `json:"total_tokens"`
	IndexTerms              int              `json:"index_terms"`
	ChunksWithImages        int              `json:"chunks_with_images"`
}

// goldenReport is one report as scripts/verify_goldens.py wrote it, and the
// text Python printed for it.
type goldenReport struct {
	Text    string         `json:"text"`
	Report  goldenFields   `json:"report"`
	Verdict domain.Verdict `json:"verdict"`
}

// DisplaySuite holds Go's verify report to the text Python prints.
type DisplaySuite struct {
	suite.Suite
}

func TestDisplay(t *testing.T) { suite.Run(t, new(DisplaySuite)) }

func (s *DisplaySuite) TestEveryGoldenReportPrintsWhatPythonPrinted() {
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "testdata", "verify", "*.json"))
	s.Require().NoError(err)
	s.Require().NotEmpty(paths, "goldens are written by scripts/verify_goldens.py")

	for _, path := range paths {
		s.Run(filepath.Base(path), func() {
			golden := s.load(path)
			report := reportOf(golden)
			s.Equal(expected(golden), report.Display())
		})
	}
}

func (s *DisplaySuite) TestADocumentWithNoPagesReadsAsADash() {
	// Python printed the literal "None" here. A dash is what `docsearch
	// list` already prints for a count a document does not have.
	report := document.VerifyReport{Document: document.Document{DocID: "notes"}}
	s.Contains(report.Display(), "pages       -")
}

func (s *DisplaySuite) TestAFindingsDetailWrapsWithoutBreakingAWord() {
	report := document.VerifyReport{Findings: []domain.Finding{{
		Code:     "boilerplate",
		Severity: domain.VerdictDegraded,
		Detail: "A line repeated on every page of a manual carries no meaning of " +
			"its own and still earns term mass in every chunk it lands in, which " +
			"is how a footer comes to answer a question about the product.",
	}}}
	for _, line := range strings.Split(report.Display(), "\n") {
		s.LessOrEqual(len(line), 82, "a wrapped line plus its indent")
	}
}

func (s *DisplaySuite) load(path string) goldenReport {
	raw, err := os.ReadFile(path)
	s.Require().NoError(err)
	var golden goldenReport
	s.Require().NoError(json.Unmarshal(raw, &golden))
	return golden
}

// expected is the golden text with the one documented divergence applied.
func expected(golden goldenReport) string {
	return strings.Replace(golden.Text, "pages       None", "pages       -", 1)
}

// reportOf rebuilds the Python dataclass as the Go report.
func reportOf(golden goldenReport) document.VerifyReport {
	r := golden.Report
	return document.VerifyReport{
		Document: document.Document{
			DocID:     r.DocID,
			Title:     r.Title,
			Format:    r.Format,
			Status:    r.Status,
			PageCount: r.PageCount,
		},
		Problems:           r.Problems,
		Findings:           r.Findings,
		UnjoinableSections: r.UnjoinableIndexSections,
		IndexTerms:         r.IndexTerms,
		Verdict:            golden.Verdict,
		Measurements: domain.Measurements{
			ChunkCount:       r.ChunkCount,
			ChunksWithImages: r.ChunksWithImages,
			UncoveredPages:   r.UncoveredPages,
			Longest:          sizedChunks(r.Longest),
			Shortest:         sizedChunks(r.Shortest),
			Tokens: domain.TokenSpread{
				Min:    r.TokenMin,
				Median: r.TokenMedian,
				P95:    r.TokenP95,
				Max:    r.TokenMax,
				Mean:   r.TokenMean,
				Total:  r.TotalTokens,
			},
		},
	}
}

// sizedChunks reads the extremes. The goldens number chunks so that an id
// equals its ordinal, since Go names an extreme chunk by ordinal where
// Python named it by row id.
func sizedChunks(chunks []goldenChunk) []domain.SizedChunk {
	out := make([]domain.SizedChunk, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, domain.SizedChunk{
			Ordinal:     c.Ordinal,
			Tokens:      c.Tokens,
			HeadingPath: c.Path,
		})
	}
	return out
}
