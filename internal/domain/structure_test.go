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
		report      *domain.StructureReport
		name        string
		wantQuality domain.Quality
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
	s.Equal("ok", payload["quality"], "a grade persists as the text v1 already stores")
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
		{name: "first corpus", chunks: 944, paths: 881, want: false},
		{name: "second corpus", chunks: 265, paths: 222, want: false},
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

// paperSections is how a paper's chunks fall under its headings: twenty
// sections, most of one chunk, two the token budget split five ways, and a
// reference list of twelve. Every heading the paper marks is a path.
var paperSections = []int{2, 1, 1, 1, 5, 2, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1, 5, 2, 2, 12}

// sectioned is a document with the given number of chunks under each of its
// headings in turn, the last of them its references.
func sectioned(counts []int) []domain.Chunk {
	var chunks []domain.Chunk
	for i, n := range counts {
		path := fmt.Sprintf("Paper > Section %d", i+1)
		if i == len(counts)-1 {
			path = "Paper > References"
		}
		for range n {
			chunks = append(chunks, domain.Chunk{
				HeadingPath: path,
				Text:        "The pump draws water through the inlet filter.",
				Ordinal:     len(chunks),
			})
		}
	}
	return chunks
}

// measured is a report from the source given over the chunks given.
func measured(source domain.StructureSource, chunks []domain.Chunk) *domain.StructureReport {
	report := &domain.StructureReport{StructureSource: source}
	report.MeasureChunks(chunks)
	return report
}

// A section the token budget split carries its path into every piece, so
// chunks outnumber paths wherever a document writes at length. Under
// headings read from markup nothing was missed, and the split is no defect.
func (s *StructureSuite) TestLongSectionsUnderMarkupHeadingsAreAddressable() {
	for _, source := range []domain.StructureSource{
		domain.SourceATXHeadings, domain.SourceHTMLNesting, domain.SourceHeadingStyles,
	} {
		s.Run(source.String(), func() {
			report := measured(source, sectioned(paperSections))
			s.Equal(44, report.Chunks)
			s.Equal(20, report.DistinctHeadingPaths)
			s.Less(report.Addressability(), domain.AddressableMin)
			s.False(report.Unaddressable())
			s.Equal(domain.QualityOK, report.Quality())
			s.Empty(report.Notes())
		})
	}
}

// The same distribution under inferred headings may be headings the
// inference missed, and the note says which source could have missed them.
func (s *StructureSuite) TestLongSectionsUnderInferredHeadingsStayDegraded() {
	report := measured(domain.SourceFontHeuristic, sectioned(paperSections))
	s.True(report.Unaddressable())
	s.False(report.Undivided())
	s.Equal(domain.QualityDegraded, report.Quality())
	notes := strings.Join(report.Notes(), " ")
	s.Contains(notes, "44 chunks share only 20 distinct heading paths (0.45 per chunk)")
	s.Contains(notes, "structure source 'font_heuristic', which can miss headings")
}

// A document most of which sits under one path is not divided by its
// structure, whatever read the headings.
func (s *StructureSuite) TestAnUndividedDocumentIsDegradedWhateverTheSource() {
	tests := []struct {
		name     string
		wantNote string
		chunks   []domain.Chunk
		source   domain.StructureSource
	}{
		{
			name:     "markup with only a title",
			source:   domain.SourceATXHeadings,
			chunks:   sectioned([]int{30}),
			wantNote: "30 of 30 chunks (100%) share one heading path, 'Paper > References'",
		},
		{
			name:     "markup with one section holding most of it",
			source:   domain.SourceATXHeadings,
			chunks:   sectioned([]int{1, 1, 1, 1, 26}),
			wantNote: "26 of 30 chunks (87%) share one heading path",
		},
		{
			name:     "an outline none of whose chunks carry a heading",
			source:   domain.SourceOutline,
			chunks:   headless(30),
			wantNote: "30 of 30 chunks (100%) carry no heading path",
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			report := measured(tt.source, tt.chunks)
			s.True(report.Undivided())
			s.True(report.Unaddressable())
			s.Equal(domain.QualityDegraded, report.Quality())
			s.Contains(strings.Join(report.Notes(), " "), tt.wantNote)
		})
	}
}

// headless is a document of n chunks none of which carries a heading path.
func headless(n int) []domain.Chunk {
	chunks := make([]domain.Chunk, n)
	for i := range chunks {
		chunks[i] = domain.Chunk{Text: "The kettle switches off once the water boils.", Ordinal: i}
	}
	return chunks
}

// Where two sections hold as many chunks, the first is named, so a document
// measured twice reads the same.
func (s *StructureSuite) TestTheLargestSectionIsTheFirstOfEquals() {
	report := measured(domain.SourceATXHeadings, sectioned([]int{1, 3, 3}))
	s.Equal(3, report.LargestSectionChunks)
	s.Equal("Paper > Section 2", report.LargestSectionPath)
	s.InDelta(3.0/7.0, report.LargestSectionShare(), 1e-9)
}

// Chunk sizes for a document in an uncalibrated script are in an unknown
// unit, though nothing looks wrong. Noted first, downgraded past a bound.
func (s *StructureSuite) TestUncalibratedScripts() {
	tests := []struct {
		name        string
		share       float64
		wantQuality domain.Quality
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
	s.Equal("degraded", payload["quality"])
	s.InEpsilon(0.286, payload["addressability"], 0)
	s.Contains(fmt.Sprint(payload["notes"]), "section_filter cannot narrow")
}

// The largest section is persisted with the rest, so a stored report shows
// the evidence an undivided grade rests on.
func (s *StructureSuite) TestTheLargestSectionReachesThePersistedPayload() {
	payload := s.persisted(measured(domain.SourceATXHeadings, sectioned([]int{1, 1, 28})))
	s.Equal("degraded", payload["quality"])
	s.InEpsilon(28, payload["largest_section_chunks"], 0)
	s.Equal("Paper > References", payload["largest_section_path"])
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
