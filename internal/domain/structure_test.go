package domain_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

// StructureSuite covers the structure-validation policy. Ported from
// tests/test_structure_policy.py and tests/test_structure_policy_addressability.py;
// the end-to-end cases there test ingest and move with it.
type StructureSuite struct{ suite.Suite }

func TestStructure(t *testing.T) { suite.Run(t, new(StructureSuite)) }

// healthy is a report clean on every axis, so a test varies exactly one thing.
func healthy() *domain.StructureReport {
	return &domain.StructureReport{
		StructureSource:      domain.SourceFrontTOC,
		TOCSections:          100,
		BodySections:         100,
		Chunks:               100,
		DistinctHeadingPaths: 90,
	}
}

// budgetSliced is a document whose chunks came from the token budget rather
// than its structure.
func budgetSliced() *domain.StructureReport {
	return &domain.StructureReport{
		StructureSource:      domain.SourceFontHeuristic,
		Chunks:               35,
		DistinctHeadingPaths: 10,
		HeadlessChunks:       4,
	}
}

func (s *StructureSuite) TestTOCAgreementDecidesFatality() {
	tests := []struct {
		name        string
		report      *domain.StructureReport
		wantQuality string
	}{
		{
			name:        "empty symmetric difference",
			report:      &domain.StructureReport{StructureSource: domain.SourceFrontTOC},
			wantQuality: domain.QualityOK,
		},
		{
			name: "missing body section",
			report: &domain.StructureReport{
				StructureSource: domain.SourceFrontTOC,
				InTOCNotInBody:  []string{"12.3"},
			},
			wantQuality: domain.QualityFailed,
		},
		{
			name: "extra body heading",
			report: &domain.StructureReport{
				StructureSource: domain.SourceFrontTOC,
				InBodyNotInTOC:  []string{"99.1"},
			},
			wantQuality: domain.QualityFailed,
		},
		// Without a table of contents there is no symmetric difference to take.
		{
			name: "font heuristic has nothing to validate against",
			report: &domain.StructureReport{
				StructureSource: domain.SourceFontHeuristic,
				InTOCNotInBody:  []string{"3"},
			},
			wantQuality: domain.QualityOK,
		},
		{
			name: "duplicates degrade rather than fail",
			report: &domain.StructureReport{
				StructureSource:      domain.SourceFrontTOC,
				DetectedMoreThanOnce: []string{"4"},
			},
			wantQuality: domain.QualityDegraded,
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.wantQuality, tt.report.Quality())
			s.Equal(tt.wantQuality == domain.QualityFailed, tt.report.Fatal())
		})
	}
}

func (s *StructureSuite) TestFailureMessageNamesTheSectionsAndTheConsequence() {
	report := &domain.StructureReport{
		StructureSource: domain.SourceFrontTOC,
		InTOCNotInBody:  []string{"12.3", "12.4"},
	}
	msg := report.FailureMessage()
	s.Contains(msg, "12.3")
	s.Contains(msg, "12.4")
	s.Contains(msg, "not indexed")
	s.Contains(msg, "table of contents")
}

// Subdivision yields adjacent ordinals; a misfired boundary leaves gaps.
func (s *StructureSuite) TestScatteredSectionsDetectsGapsNotSubdivisions() {
	chunk := func(section string, ordinal int) domain.Chunk {
		return domain.Chunk{Section: str(section), Ordinal: ordinal}
	}
	subdivided := []domain.Chunk{chunk("7.1", 4), chunk("7.1", 5), chunk("7.1", 6)}
	s.Empty(domain.ScatteredSections(subdivided))

	misfired := []domain.Chunk{chunk("1", 0), chunk("1", 148), chunk("1", 226)}
	s.Equal([]string{"1"}, domain.ScatteredSections(misfired))
}

func (s *StructureSuite) TestDiagnosticsRoundTripThroughJSON() {
	report := domain.NewStructureReport(map[string]any{
		"structure_source":                "front_toc",
		"candidates_rejected_by_ordering": []any{"p315:1"},
		"cross_validation": map[string]any{
			"toc_sections":            827.0,
			"body_sections":           827.0,
			"in_toc_not_in_body":      []any{},
			"in_body_not_in_toc":      []any{},
			"detected_more_than_once": []any{},
		},
	})
	payload := s.persisted(report)
	s.Equal(domain.QualityOK, payload["quality"])
	s.InEpsilon(827.0, payload["toc_sections"], 0)
	s.Equal([]any{"p315:1"}, payload["candidates_rejected_by_ordering"])
}

// Two empty sets agree on everything, so a document from which nothing was
// derived must not pass as cross-validated.
func (s *StructureSuite) TestEmptyAgreementIsNotCrossValidation() {
	report := &domain.StructureReport{StructureSource: domain.SourceFrontTOC}
	s.Empty(report.SymmetricDifference())
	s.False(report.CrossValidated())
	s.Contains(strings.Join(report.Notes(), " "), "not cross-validated")
}

func (s *StructureSuite) TestRealAgreementIsCrossValidation() {
	report := &domain.StructureReport{
		StructureSource: domain.SourceFrontTOC,
		TOCSections:     827,
		BodySections:    827,
	}
	s.True(report.CrossValidated())
	s.Equal(domain.QualityOK, report.Quality())
}

func (s *StructureSuite) TestDisagreementStillFails() {
	missing := make([]string, 27)
	for i := range missing {
		missing[i] = fmt.Sprint(i)
	}
	report := &domain.StructureReport{
		StructureSource: domain.SourceFrontTOC,
		TOCSections:     827,
		BodySections:    800,
		InTOCNotInBody:  missing,
	}
	s.Equal(domain.QualityFailed, report.Quality())
}

// An embedded outline declares sections, nesting and position. Requiring
// font-detected body headings to confirm it would reject documents whose
// headings are styled by weight rather than size.
func (s *StructureSuite) TestAnAuthoritativeSourceNeedsNoCorroboration() {
	report := &domain.StructureReport{StructureSource: domain.SourceOutline}
	s.True(report.Authoritative())
	s.False(report.Validatable())
	s.False(report.Fatal())
}

func (s *StructureSuite) TestBudgetSlicedStructureIsDegradedWhateverProducedIt() {
	report := budgetSliced()
	s.True(report.Unaddressable())
	s.Equal(domain.QualityDegraded, report.Quality())
	notes := strings.Join(report.Notes(), " ")
	s.Contains(notes, "35 chunks share only 10")
	s.Contains(notes, "4 chunk(s) carry no heading path")
}

// One headless chunk in 944 is a blemish, not a symptom; downgrading for it
// would report a corpus that retrieves correctly as suspect.
func (s *StructureSuite) TestOneUnreachableChunkDoesNotDowngradeAWholeDocument() {
	report := &domain.StructureReport{
		StructureSource:      domain.SourceFrontTOC,
		TOCSections:          827,
		BodySections:         827,
		Chunks:               944,
		DistinctHeadingPaths: 881,
		HeadlessChunks:       1,
	}
	s.False(report.MostlyHeadless())
	s.Equal(domain.QualityOK, report.Quality())
	s.Contains(strings.Join(report.Notes(), " "), "1 chunk(s) carry no heading path")
}

// Guards the bound against the corpora known to retrieve correctly, and
// requires a distribution before judging one.
func (s *StructureSuite) TestAddressability() {
	tests := []struct {
		name          string
		chunks, paths int
		want          bool
	}{
		{name: "grandMA2", chunks: 944, paths: 881, want: false},
		{name: "QLC+", chunks: 265, paths: 222, want: false},
		{name: "third corpus", chunks: 318, paths: 257, want: false},
		{name: "too few chunks to judge", chunks: 4, paths: 1, want: false},
		{name: "budget-sliced", chunks: 35, paths: 10, want: true},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			report := &domain.StructureReport{
				StructureSource:      domain.SourceFrontTOC,
				Chunks:               tt.chunks,
				DistinctHeadingPaths: tt.paths,
			}
			s.Equal(tt.want, report.Unaddressable())
		})
	}
}

// Chunk sizes for a document in an uncalibrated script are in an unknown
// unit, though nothing looks wrong. Noted first, downgraded past a bound.
func (s *StructureSuite) TestUncalibratedScripts() {
	tests := []struct {
		name        string
		wantQuality string
		share       float64
		wantNote    bool
	}{
		{name: "calibrated", share: 0, wantNote: false, wantQuality: domain.QualityOK},
		{name: "noted", share: 0.2, wantNote: true, wantQuality: domain.QualityOK},
		{name: "downgraded", share: 0.95, wantNote: true, wantQuality: domain.QualityDegraded},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			report := healthy()
			report.UncalibratedScriptShare = tt.share
			noted := strings.Contains(strings.Join(report.Notes(), " "), "unknown unit")
			s.Equal(tt.wantNote, noted)
			s.Equal(tt.wantQuality, report.Quality())
		})
	}
}

func (s *StructureSuite) TestAHealthyDocumentHasNoNotes() {
	s.Empty(healthy().Notes())
}

// A worker is headless; a finding that is not serialised is lost.
func (s *StructureSuite) TestNotesAndAddressabilityReachThePersistedPayload() {
	payload := s.persisted(budgetSliced())
	s.Equal(domain.QualityDegraded, payload["quality"])
	s.InEpsilon(0.286, payload["addressability"], 0)
	s.Contains(fmt.Sprint(payload["notes"]), "section_filter cannot narrow")
}

// The Python payload never carries a null list, and the MCP server reads it
// back.
func (s *StructureSuite) TestThePersistedPayloadHasNoNullLists() {
	payload := s.persisted(&domain.StructureReport{StructureSource: domain.SourceOutline})
	keys := []string{"in_toc_not_in_body", "scattered_sections", "notes", "placed_by_path"}
	for _, key := range keys {
		s.Equal([]any{}, payload[key], key)
	}
}

// persisted decodes a report's persisted JSON.
func (s *StructureSuite) persisted(report *domain.StructureReport) map[string]any {
	raw, err := report.JSON()
	s.Require().NoError(err)
	var payload map[string]any
	s.Require().NoError(json.Unmarshal(raw, &payload))
	return payload
}
