package domain_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

// goldenInspect is what scripts/inspect_goldens.py wrote for one fixture:
// what Python found, and the text Python printed for it.
type goldenInspect struct {
	Format          string                 `json:"format"`
	PredictedSource string                 `json:"predicted_source"`
	PredictedTier   string                 `json:"predicted_tier"`
	PageCount       *int                   `json:"page_count"`
	Text            string                 `json:"text"`
	Findings        []goldenInspectFinding `json:"findings"`
}

type goldenInspectFinding struct {
	Label  string       `json:"label"`
	Detail string       `json:"detail"`
	Level  domain.Level `json:"level"`
}

// InspectReportSuite holds the printed reconnaissance to what Python prints.
//
// The findings themselves are compared in internal/adapter/pdf, against what
// the Go engine reads. This asks the narrower question the CLI depends on:
// given one set of findings, do the two print the same page.
type InspectReportSuite struct {
	suite.Suite
}

func TestInspectReport(t *testing.T) { suite.Run(t, new(InspectReportSuite)) }

func (s *InspectReportSuite) TestEveryGoldenPrintsWhatPythonPrinted() {
	dir := filepath.Join("..", "..", "testdata", "inspect")
	entries, err := os.ReadDir(dir)
	s.Require().NoError(err)
	s.Require().NotEmpty(entries, "goldens are written by scripts/inspect_goldens.py")

	for _, entry := range entries {
		s.Run(entry.Name(), func() {
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			s.Require().NoError(err)
			var golden goldenInspect
			s.Require().NoError(json.Unmarshal(raw, &golden))
			s.Require().NotEmpty(golden.Text)

			report := domain.InspectReport{
				// The golden stores no path, and the report prints a file by
				// its name, so the fixture's name is all it needs.
				Target:          strings.TrimSuffix(entry.Name(), ".json"),
				Format:          golden.Format,
				PredictedSource: golden.PredictedSource,
				PredictedTier:   golden.PredictedTier,
				PageCount:       golden.PageCount,
			}
			for _, f := range golden.Findings {
				report.Add(f.Level, f.Label, f.Detail)
			}
			s.Equal(golden.Text, report.Report())
		})
	}
}

func (s *InspectReportSuite) TestABlockedDocumentSaysSoInsteadOfNamingASource() {
	report := domain.InspectReport{
		Target:          "scan.pdf",
		Format:          "pdf",
		PredictedSource: "unknown",
		PredictedTier:   domain.TierInferred,
	}
	report.Add(domain.LevelBlocked, "text layer", "absent on every page")
	s.Contains(report.Report(), "This document cannot be ingested as it stands.")
	s.NotContains(report.Report(), "structure source:")
}

func (s *InspectReportSuite) TestASiteCountsPagesItFound() {
	pages := 42
	report := domain.InspectReport{
		Target:          "https://example.test/docs",
		Format:          "site",
		PredictedSource: "sidebar_dom",
		PredictedTier:   domain.TierDeclared,
		PageCount:       &pages,
	}
	printed := report.Report()
	s.Contains(printed, "site        https://example.test/docs")
	s.Contains(printed, "pages found 42")
}
