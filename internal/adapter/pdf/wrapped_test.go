package pdf_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/adapter/pdf"
)

// WrappedSuite checks headings long enough to wrap onto a second line: one
// heading, never two, and never part of the text below it.
type WrappedSuite struct{ suite.Suite }

func TestWrapped(t *testing.T) { suite.Run(t, new(WrappedSuite)) }

func (s *WrappedSuite) TestAnOutlineTitleIsFoundOverTheLinesItWrapsOnto() {
	lines := []pdf.Line{
		line("Opening", 50, 40, 14),
		line("Opening text.", 50, 60, 10),
		line("Adjusting the Output Level via", 50, 100, 14),
		line("the Front Panel", 50, 118, 14),
		line("Turn the knob.", 50, 140, 10),
	}
	doc := &pdf.Document{
		Outline: []pdf.OutlineEntry{
			{Title: "Opening", Level: 1, Page: 1},
			{Title: "Adjusting the Output Level via the Front Panel", Level: 1, Page: 1},
		},
		Pages: []pdf.Page{{Lines: lines}},
	}
	got, err := pdf.Build(doc, "wrapped.pdf")
	s.Require().NoError(err)
	s.Equal(map[string]string{
		"Opening": "Opening text.",
		"Adjusting the Output Level via the Front Panel": "Turn the knob.",
	}, bodyText(got.Blocks))
	placement, ok := got.Diagnostics["outline_placement"].(map[string]any)
	s.Require().True(ok)
	s.Equal(2, placement["located_by_title"])
}

func (s *WrappedSuite) TestAnOutlineTitleDoesNotRunIntoTheTextBelow() {
	// The first line starts the title, but what follows is body text.
	lines := []pdf.Line{
		line("Adjusting the Output Level via", 50, 100, 10),
		line("a menu is described below.", 50, 112, 10),
	}
	doc := &pdf.Document{
		Outline: []pdf.OutlineEntry{
			{Title: "Adjusting the Output Level via the Front Panel", Level: 1, Page: 1},
		},
		Pages: []pdf.Page{{Lines: lines}},
	}
	got, err := pdf.Build(doc, "unwrapped.pdf")
	s.Require().NoError(err)
	s.Equal(map[string]string{
		"Adjusting the Output Level via the Front Panel": "Adjusting the Output Level via\na menu is described below.",
	}, bodyText(got.Blocks))
}

func (s *WrappedSuite) TestANumberedTitleTakesTheLineItWrapsOnto() {
	var pages []pdf.Page
	for i := range 5 {
		n := strconv.Itoa(i + 1)
		pages = append(pages, pdf.Page{Lines: []pdf.Line{
			line(n+". Replacing the drive belt on", 50, 60, 16),
			line("machine "+n, 50, 80, 16),
			line("Body text of section "+n+".", 50, 110, 10),
			line("More body text that runs on for a while.", 50, 122, 10),
		}})
	}
	got, err := pdf.Build(&pdf.Document{Pages: pages}, "numbered.pdf")
	s.Require().NoError(err)
	s.Equal("Body text of section 2.\nMore body text that runs on for a while.",
		bodyText(got.Blocks)["2. Replacing the drive belt on machine 2"])
}

func (s *WrappedSuite) TestANumberedTitleStopsAtTheNextNumberedLine() {
	var pages []pdf.Page
	for i := range 5 {
		n := strconv.Itoa(2*i + 1)
		m := strconv.Itoa(2*i + 2)
		pages = append(pages, pdf.Page{Lines: []pdf.Line{
			line(n+". Short", 50, 60, 16),
			line(m+". Next", 50, 80, 16),
			line("Body text of section "+m+".", 50, 110, 10),
			line("More body text that runs on for a while.", 50, 122, 10),
		}})
	}
	got, err := pdf.Build(&pdf.Document{Pages: pages}, "adjacent.pdf")
	s.Require().NoError(err)
	s.Equal("Body text of section 4.\nMore body text that runs on for a while.",
		bodyText(got.Blocks)["4. Next"])
}
