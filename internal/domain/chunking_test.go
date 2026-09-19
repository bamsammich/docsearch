package domain_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

// ChunkingSuite exercises the chunker through the Extraction the adapters
// emit. Ported from tests/test_chunker.py.
type ChunkingSuite struct{ suite.Suite }

func TestChunking(t *testing.T) { suite.Run(t, new(ChunkingSuite)) }

func extraction(blocks ...domain.Block) domain.Extraction {
	return domain.Extraction{Title: "t", Format: "test", Blocks: blocks}
}

func para(n int) string { return strings.Repeat("word ", n) }

func str(s string) *string { return &s }

func num(n int) *int { return &n }

func page(n int) map[string]int { return map[string]int{"page": n} }

func offset(n int) map[string]int { return map[string]int{"offset": n} }

func sections(chunks []domain.Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		if c.Section != nil {
			out[i] = *c.Section
		}
	}
	return out
}

func headingPaths(chunks []domain.Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.HeadingPath
	}
	return out
}

func kinds(chunks []domain.Chunk) map[string]int {
	out := map[string]int{}
	for _, c := range chunks {
		out[c.Kind]++
	}
	return out
}

func referenceFamily(parent string, n int, leaf string) []domain.Chunk {
	chunks := make([]domain.Chunk, n)
	for i := range chunks {
		chunks[i] = domain.Chunk{
			Ordinal: i,
			HeadingPath: strings.Join(
				[]string{"Manual", parent, fmt.Sprintf("%s %d", leaf, i)},
				domain.PathSep,
			),
			Text: strings.Repeat(fmt.Sprintf("Definition of term %d. ", i), 8),
			Kind: domain.KindProse,
		}
	}
	return chunks
}

// assertUnderCap checks every chunk fits MaxTokens.
func (s *ChunkingSuite) assertUnderCap(chunks []domain.Chunk) {
	for _, c := range chunks {
		s.LessOrEqual(domain.EstimateTokens(c.Text), domain.MaxTokens, c.HeadingPath)
	}
}

// A declared section boundary survives even when both sides are tiny.
func (s *ChunkingSuite) TestNumberedSectionsAreNeverMergedHoweverSmall() {
	chunks := domain.Chunks(extraction(
		domain.Block{
			HeadingPath: []string{"1. A", "1.1. First"},
			Locator:     page(1),
			Text:        "tiny",
			Section:     str("1.1"),
		},
		domain.Block{
			HeadingPath: []string{"1. A", "1.2. Second"},
			Locator:     page(1),
			Text:        "also",
			Section:     str("1.2"),
		},
	))
	s.Equal([]string{"1.1", "1.2"}, sections(chunks))
	for _, c := range chunks {
		s.Less(domain.EstimateTokens(c.Text), domain.MinTokens)
	}
}

func (s *ChunkingSuite) TestUnnumberedSmallBlocksMerge() {
	tests := []struct {
		name       string
		first      []string
		second     []string
		wantChunks int
	}{
		{
			name:       "forward under a shared parent",
			first:      []string{"Top", "One"},
			second:     []string{"Top", "Two"},
			wantChunks: 1,
		},
		{
			name:       "never across parents",
			first:      []string{"Alpha", "One"},
			second:     []string{"Beta", "Two"},
			wantChunks: 2,
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			chunks := domain.Chunks(extraction(
				domain.Block{HeadingPath: tt.first, Locator: offset(0), Text: "tiny"},
				domain.Block{HeadingPath: tt.second, Locator: offset(1), Text: "small"},
			))
			s.Len(chunks, tt.wantChunks)
		})
	}
}

func (s *ChunkingSuite) TestOversizedSectionSubdividesUnderTheCap() {
	var blocks []domain.Block
	for i := range 4 {
		blocks = append(blocks, domain.Block{
			HeadingPath: []string{"1. A"},
			Locator:     page(1),
			Text:        para(900),
			Section:     str("1"),
			Subdivision: i > 0,
		})
	}
	chunks := domain.Chunks(extraction(blocks...))
	s.Greater(len(chunks), 1)
	s.assertUnderCap(chunks)
	for _, section := range sections(chunks) {
		s.Equal("1", section)
	}
}

// A section with no interior heading must still respect the cap.
func (s *ChunkingSuite) TestSingleOversizedBlockSplitsAtParagraphBoundaries() {
	paras := make([]string, 12)
	for i := range paras {
		paras[i] = para(300)
	}
	chunks := domain.Chunks(extraction(domain.Block{
		HeadingPath: []string{"1. A"},
		Locator:     page(1),
		Text:        strings.Join(paras, "\n\n"),
		Section:     str("1"),
	}))
	s.Greater(len(chunks), 1)
	s.assertUnderCap(chunks)
}

// Under the cap a section stays whole even with subheadings inside it.
func (s *ChunkingSuite) TestSubdivisionDoesNotFireBelowTheCap() {
	chunks := domain.Chunks(extraction(
		domain.Block{
			HeadingPath: []string{"1. A"},
			Locator:     page(1),
			Text:        para(20),
			Section:     str("1"),
		},
		domain.Block{
			HeadingPath: []string{"1. A"},
			Locator:     page(1),
			Text:        para(20),
			Section:     str("1"),
			Subdivision: true,
		},
	))
	s.Len(chunks, 1)
}

func (s *ChunkingSuite) TestOrdinalsAreDenseAndAscending() {
	var blocks []domain.Block
	for i := 1; i < 8; i++ {
		blocks = append(blocks, domain.Block{
			HeadingPath: []string{fmt.Sprintf("%d. S", i)},
			Locator:     page(i),
			Text:        para(60),
			Section:     str(fmt.Sprint(i)),
		})
	}
	for i, c := range domain.Chunks(extraction(blocks...)) {
		s.Equal(i, c.Ordinal)
	}
}

func (s *ChunkingSuite) TestHeadingPathIsFullAncestryJoined() {
	chunks := domain.Chunks(extraction(domain.Block{
		HeadingPath: []string{"5. System", "5.2. Units", "5.2.1. RPU"},
		Locator:     page(3),
		Text:        para(50),
		Section:     str("5.2.1"),
	}))
	s.Require().Len(chunks, 1)
	s.Equal("5. System > 5.2. Units > 5.2.1. RPU", chunks[0].HeadingPath)
}

func (s *ChunkingSuite) TestLocatorAndImageCountSurviveIntoTheChunk() {
	chunks := domain.Chunks(extraction(
		domain.Block{
			HeadingPath: []string{"1. A"},
			Locator:     map[string]int{"page": 10, "page_end": 11},
			Text:        para(50),
			Section:     str("1"),
			PrintedPage: num(9),
			ImageCount:  3,
		},
		domain.Block{
			HeadingPath: []string{"1. A"},
			Locator:     map[string]int{"page": 11, "page_end": 12},
			Text:        para(50),
			Section:     str("1"),
			PrintedPage: num(10),
			ImageCount:  2,
		},
	))
	s.Require().Len(chunks, 1)
	c := chunks[0]
	s.Require().NotNil(c.PageStart)
	s.Require().NotNil(c.PageEnd)
	s.Require().NotNil(c.PrintedPageStart)
	s.Equal(10, *c.PageStart)
	s.Equal(12, *c.PageEnd)
	s.Equal(9, *c.PrintedPageStart)
	s.Equal(5, c.ImageCount)
}

// Budgeting must accumulate atoms, not summed token estimates: estimates
// truncate, and summed over hundreds of short lines the cap never trips.
func (s *ChunkingSuite) TestManyShortLinesStillSplitAtTheCap() {
	lines := make([]string, 800)
	for i := range lines {
		lines[i] = "Decimal 255 equals hex FF and octal 377"
	}
	body := strings.Join(lines, "\n")
	chunks := domain.Chunks(extraction(domain.Block{
		HeadingPath: []string{"1. Table"},
		Locator:     page(1),
		Text:        body,
		Section:     str("1"),
	}))
	s.Require().Greater(domain.EstimateTokens(body), domain.MaxTokens)
	s.Greater(len(chunks), 1)
	s.assertUnderCap(chunks)
}

func (s *ChunkingSuite) TestChapterStubs() {
	console := domain.Block{
		HeadingPath: []string{"4. Devices", "4.1. Console"},
		Locator:     page(1),
		Text:        para(120),
		Section:     str("4.1"),
	}
	tests := []struct {
		name         string
		wantSections []string
		parent       domain.Block
		next         domain.Block
	}{
		{
			name: "a thin preamble folds into its first child",
			parent: domain.Block{
				HeadingPath: []string{"4. Devices"},
				Locator:     page(1),
				Text:        "A short lead-in.",
				Section:     str("4"),
			},
			next:         console,
			wantSections: []string{"4.1"},
		},
		{
			name: "a substantial parent keeps its own chunk",
			parent: domain.Block{
				HeadingPath: []string{"4. Devices"},
				Locator:     page(1),
				Text:        para(200),
				Section:     str("4"),
			},
			next:         console,
			wantSections: []string{"4", "4.1"},
		},
		{
			name: "a short section before a sibling is not a stub",
			parent: domain.Block{
				HeadingPath: []string{"4. Devices"},
				Locator:     page(1),
				Text:        "Short.",
				Section:     str("4"),
			},
			next: domain.Block{
				HeadingPath: []string{"5. Network"},
				Locator:     page(2),
				Text:        para(120),
				Section:     str("5"),
			},
			wantSections: []string{"4", "5"},
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.wantSections, sections(domain.Chunks(extraction(tt.parent, tt.next))))
		})
	}
}

func (s *ChunkingSuite) TestAFoldedStubKeepsItsText() {
	chunks := domain.Chunks(extraction(
		domain.Block{
			HeadingPath: []string{"4. Devices"},
			Locator:     page(1),
			Text:        "A short lead-in.",
			Section:     str("4"),
		},
		domain.Block{
			HeadingPath: []string{"4. Devices", "4.1. Console"},
			Locator:     page(1),
			Text:        para(120),
			Section:     str("4.1"),
		},
	))
	s.Require().Len(chunks, 1)
	s.Contains(chunks[0].Text, "A short lead-in.")
}

func (s *ChunkingSuite) TestClassifyKinds() {
	described := make([]domain.Chunk, 40)
	for i := range described {
		described[i] = domain.Chunk{
			HeadingPath: strings.Join([]string{
				"Manual", "Glossary", fmt.Sprintf("How to configure the %d subsystem correctly", i),
			}, domain.PathSep),
			Text: strings.Repeat("Prose. ", 20),
			Kind: domain.KindProse,
		}
	}
	tests := []struct {
		name   string
		want   string
		chunks []domain.Chunk
	}{
		// Families were once keyed on section numbers, which left any
		// unnumbered document's declared glossary unreachable.
		{
			name:   "a glossary in an unnumbered document",
			chunks: referenceFamily("Glossary", 40, "Entry"),
			want:   domain.KindKeywordReference,
		},
		// Only the parent heading tells a listing from a topic: 84 sibling
		// pages about physical keys look like an index by every other measure.
		{
			name:   "a topic chapter of uniform siblings",
			chunks: referenceFamily("Keys & Buttons on the Console", 84, "Key"),
			want:   domain.KindProse,
		},
		{
			name:   "a small declared family",
			chunks: referenceFamily("Glossary", 12, "Entry"),
			want:   domain.KindProse,
		},
		{name: "entries described in sentences", chunks: described, want: domain.KindProse},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			domain.ClassifyKinds(tt.chunks)
			s.Equal(map[string]int{tt.want: len(tt.chunks)}, kinds(tt.chunks))
		})
	}
}

// A long entry that subdivision split sits one level deeper than its siblings
// and must stay with its family rather than form one of its own.
func (s *ChunkingSuite) TestSubdividedEntriesStayWithTheirFamily() {
	chunks := referenceFamily("All keywords", 40, "Entry")
	for i, part := range []string{"Syntax", "Options", "Examples"} {
		chunks = append(chunks, domain.Chunk{
			Ordinal: 100 + i,
			HeadingPath: strings.Join(
				[]string{"Manual", "All keywords", "Clone keyword", part},
				domain.PathSep,
			),
			Text: strings.Repeat("Detail. ", 20),
			Kind: domain.KindProse,
		})
	}
	domain.ClassifyKinds(chunks)
	s.Equal(map[string]int{domain.KindKeywordReference: len(chunks)}, kinds(chunks))
}

// A section held constant across differing headings, as a site's page is,
// must keep each heading's path rather than collapse to the first.
func (s *ChunkingSuite) TestOneSectionKeepsItsInternalHeadingPaths() {
	chunks := domain.Chunks(extraction(
		domain.Block{
			HeadingPath: []string{"Page"},
			Locator:     offset(0),
			Text:        para(120),
			Section:     str("3.2"),
		},
		domain.Block{
			HeadingPath: []string{"Page", "Install"},
			Locator:     offset(1),
			Text:        para(120),
			Section:     str("3.2"),
		},
		domain.Block{
			HeadingPath: []string{"Page", "Configure"},
			Locator:     offset(2),
			Text:        para(120),
			Section:     str("3.2"),
		},
	))
	s.Equal([]string{"Page", "Page > Install", "Page > Configure"}, headingPaths(chunks))
	s.Equal([]string{"3.2", "3.2", "3.2"}, sections(chunks))
}

// Headings inside one section are mergeable; a declared section boundary is
// not. Merging proceeds in pairs, since the merged unit takes its parent's
// path, so the first case yields fewer chunks than blocks rather than one.
func (s *ChunkingSuite) TestSmallUnitsMergeWithinASectionButNeverAcross() {
	var samePage []domain.Block
	for i, h := range []string{"Install", "Configure", "Run"} {
		samePage = append(samePage, domain.Block{
			HeadingPath: []string{"Page", h},
			Locator:     offset(i),
			Text:        "tiny bit of text",
			Section:     str("3.2"),
		})
	}
	merged := domain.Chunks(extraction(samePage...))
	s.Less(len(merged), len(samePage))
	for _, section := range sections(merged) {
		s.Equal("3.2", section)
	}

	across := domain.Chunks(extraction(
		domain.Block{
			HeadingPath: []string{"Page A"},
			Locator:     offset(0),
			Text:        "tiny",
			Section:     str("3.1"),
		},
		domain.Block{
			HeadingPath: []string{"Page B"},
			Locator:     offset(1),
			Text:        "also tiny",
			Section:     str("3.2"),
		},
	))
	s.Equal([]string{"3.1", "3.2"}, sections(across))
}

// Same section and same path is still one unit, as for any paginated format.
func (s *ChunkingSuite) TestAPaginatedSectionIsOneUnit() {
	var blocks []domain.Block
	for range 4 {
		blocks = append(blocks, domain.Block{
			HeadingPath: []string{"5. Sys", "5.2. Units"},
			Locator:     page(3),
			Text:        para(40),
			Section:     str("5.2"),
		})
	}
	chunks := domain.Chunks(extraction(blocks...))
	s.Require().Len(chunks, 1)
	s.Equal("5. Sys > 5.2. Units", chunks[0].HeadingPath)
}
