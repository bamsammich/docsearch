package domain_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

// SummarySuite covers how a stored report reads back to a caller.
type SummarySuite struct{ suite.Suite }

func TestSummary(t *testing.T) { suite.Run(t, new(SummarySuite)) }

// A finding captured at ingest is worthless if it stops at the database. The
// worker is headless, so this is the only way a caller learns that a
// document's structure does not separate its own chunks.
func (s *SummarySuite) TestNotesAndFindingsReachTheCaller() {
	quality, notes := domain.Summary(`{
		"quality": "degraded",
		"notes": ["35 chunks share only 10 distinct heading paths (0.29 per chunk)"],
		"scattered_sections": ["4.2"]
	}`)
	s.Equal(domain.QualityDegraded, quality)
	joined := strings.Join(notes, "\n")
	s.Contains(joined, "35 chunks share only 10 distinct heading paths")
	s.Contains(joined, "1 sections spanning non-adjacent chunks: 4.2")
}

func (s *SummarySuite) TestALongFindingNamesTenAndCountsTheRest() {
	_, notes := domain.Summary(`{"quality":"ok","in_toc_not_in_body":
		["1","2","3","4","5","6","7","8","9","10","11","12"]}`)
	s.Require().Len(notes, 1)
	s.Contains(notes[0], "12 sections in the table of contents but not the body")
	s.Contains(notes[0], "(+2 more)")
	s.NotContains(notes[0], "11")
}

// No report, or one that cannot be read, is ungraded rather than an error:
// the document is still searchable.
func (s *SummarySuite) TestNoReportIsUngraded() {
	for _, stored := range []string{"", "not json", `{"quality":"brilliant"}`} {
		quality, _ := domain.Summary(stored)
		s.Zero(quality, stored)
	}
}
