package pdf_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/adapter/pdf"
)

// BuildSuite checks Build on documents the goldens do not hold.
type BuildSuite struct{ suite.Suite }

func TestBuild(t *testing.T) { suite.Run(t, new(BuildSuite)) }

func (s *BuildSuite) TestRefusesADocumentWithNoStructure() {
	// No outline, no contents page, and every line the same size.
	page := pdf.Page{Lines: []pdf.Line{
		{Text: "Plain prose with no headings.", Y0: 90, X0: 72, Y1: 100, Size: 10},
		{Text: "More plain prose.", Y0: 112, X0: 72, Y1: 122, Size: 10},
	}}
	_, err := pdf.Build(&pdf.Document{Pages: []pdf.Page{page}}, "flat.pdf")
	s.Require().ErrorIs(err, pdf.ErrNoStructure)
	s.Contains(err.Error(), "flat.pdf: no outline tree")
}

func (s *BuildSuite) TestTitleFallsBackToTheFileName() {
	doc := &pdf.Document{
		Title:   "   ",
		Outline: []pdf.OutlineEntry{{Title: "Only", Level: 1, Page: 1}},
		Pages:   []pdf.Page{{Lines: []pdf.Line{{Text: "Only", Y0: 90, X0: 72, Y1: 100, Size: 10}}}},
	}
	got, err := pdf.Build(doc, "quick-start.pdf")
	s.Require().NoError(err)
	s.Equal("quick-start", got.Title)
}

func (s *BuildSuite) TestNumbersOutlineEntriesByNesting() {
	// A level-3 entry under a level-1 pads the skipped level, as Python does.
	lines := []pdf.Line{
		{Text: "Alpha", Y0: 90, X0: 72, Y1: 100, Size: 10},
		{Text: "Gamma", Y0: 120, X0: 72, Y1: 130, Size: 10},
		{Text: "Body text under gamma.", Y0: 150, X0: 72, Y1: 160, Size: 10},
	}
	doc := &pdf.Document{
		Outline: []pdf.OutlineEntry{
			{Title: "Alpha", Level: 1, Page: 1},
			{Title: "Gamma", Level: 3, Page: 1},
		},
		Pages: []pdf.Page{{Lines: lines}},
	}
	got, err := pdf.Build(doc, "nested.pdf")
	s.Require().NoError(err)
	s.Require().Len(got.Blocks, 1)
	s.Equal("1.0.1", *got.Blocks[0].Section)
	// The padded level has no title, so it is left out of the path.
	s.Equal([]string{"Alpha", "Gamma"}, got.Blocks[0].HeadingPath)
}
