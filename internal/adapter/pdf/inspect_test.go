package pdf_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/domain"
)

// InspectSuite holds the reconnaissance to what Python's makes of the same
// PDFs.
//
// PDFium and MuPDF do not read a page the same way, so the two halves of the
// PDF adapter are checked differently. The questions reconnaissance asks are
// the half that does match: a text layer either exists or does not, an
// outline either was authored or was not, and a size either stands above the
// body text or does not.
type InspectSuite struct {
	suite.Suite
	engine *pdf.Extractor
}

func TestInspect(t *testing.T) { suite.Run(t, new(InspectSuite)) }

func (s *InspectSuite) SetupSuite() {
	engine, err := pdf.New()
	s.Require().NoError(err)
	s.engine = engine
}

func (s *InspectSuite) TearDownSuite() {
	if s.engine != nil {
		s.Require().NoError(s.engine.Close())
	}
}

// goldenReport is what Python reported for one fixture.
type goldenReport struct {
	Format          string          `json:"format"`
	PredictedSource string          `json:"predicted_source"`
	PredictedTier   string          `json:"predicted_tier"`
	PageCount       *int            `json:"page_count"`
	Findings        []goldenFinding `json:"findings"`
	Blocked         bool            `json:"blocked"`
}

// goldenFinding is one finding as Python reports it.
type goldenFinding struct {
	Level  string `json:"level"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

func (s *InspectSuite) TestEveryFixtureReportsWhatPythonReports() {
	dir := filepath.Join("..", "..", "..", "testdata", "inspect")
	entries, err := os.ReadDir(dir)
	s.Require().NoError(err)
	s.Require().NotEmpty(entries)

	for _, entry := range entries {
		s.Run(entry.Name(), func() {
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			s.Require().NoError(err)
			var want goldenReport
			s.Require().NoError(json.Unmarshal(raw, &want))

			fixture := filepath.Join(
				"..", "..", "..", "testdata", "adapters",
				strings.TrimSuffix(entry.Name(), ".json"))
			doc, err := s.engine.Read(context.Background(), fixture)
			s.Require().NoError(err)

			got := &domain.InspectReport{
				Target: fixture, Format: "pdf", PredictedSource: "unknown",
			}
			pdf.Inspect(doc, got)

			s.Equal(want.PredictedSource, got.PredictedSource)
			s.Equal(want.PredictedTier, got.PredictedTier)
			s.Equal(want.Blocked, got.Blocked())
			s.Require().NotNil(got.PageCount)
			s.Equal(want.PageCount, got.PageCount)

			s.Require().Len(got.Findings, len(want.Findings))
			for i, finding := range got.Findings {
				s.Equal(want.Findings[i].Label, finding.Label)
				s.Equal(want.Findings[i].Level, finding.Level.String())
				s.Equal(want.Findings[i].Detail, finding.Detail)
			}
		})
	}
}

func (s *InspectSuite) TestADocumentWithNoTextLayerIsBlocked() {
	// Page images: extraction has nothing to read, and OCR is the
	// prerequisite rather than a tuning problem.
	report := &domain.InspectReport{Target: "scanned.pdf", Format: "pdf"}
	pdf.Inspect(&pdf.Document{Pages: make([]pdf.Page, 10)}, report)

	s.True(report.Blocked())
	s.Contains(report.Findings[0].Detail, "ocrmypdf")
	s.Equal(domain.LevelBlocked, report.Findings[0].Level)
}

func (s *InspectSuite) TestNoStructureAtAllIsBlocked() {
	// No outline, no printed contents, no font hierarchy: ingest refuses
	// rather than cutting the text into fixed windows.
	pages := make([]pdf.Page, 3)
	for i := range pages {
		pages[i].Lines = []pdf.Line{{Text: "Body text at one size throughout.", Size: 10}}
	}
	report := &domain.InspectReport{Target: "flat.pdf", Format: "pdf"}
	pdf.Inspect(&pdf.Document{Pages: pages}, report)

	s.True(report.Blocked())
	blocked := 0
	for _, f := range report.Findings {
		if f.Level == domain.LevelBlocked {
			blocked++
			s.Equal("structure", f.Label)
		}
	}
	s.Equal(1, blocked)
}
