package pdf_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/domain"
)

// ReadingSuite checks that Build follows a page's lines in the order they
// are read, which for a page in columns is not the order of their tops.
type ReadingSuite struct{ suite.Suite }

func TestReading(t *testing.T) { suite.Run(t, new(ReadingSuite)) }

// line is a 10pt body line, or a heading-sized one when size says so.
func line(text string, x, y, size float64) pdf.Line {
	return pdf.Line{Text: text, X0: x, Y0: y, Y1: y + size, Size: size}
}

// bodyText is every block's text, by heading path.
func bodyText(blocks []domain.Block) map[string]string {
	out := map[string]string{}
	for _, b := range blocks {
		key := strings.Join(b.HeadingPath, " > ")
		out[key] = strings.TrimSpace(out[key] + "\n" + b.Text)
	}
	return out
}

func (s *ReadingSuite) TestALetterlessLineIsFurnitureOnlyAtOneHeight() {
	// A reference list sets each "12:34-56" on a line of its own, at
	// whatever height the entry ends; the page number sits at one height.
	var pages []pdf.Page
	for i := range 10 {
		n := strconv.Itoa(i + 1)
		pages = append(pages, pdf.Page{Lines: []pdf.Line{
			line("Author "+n+". A paper title. J. Things.", 72, 100+float64(i)*20, 10),
			line(n+":10"+n+"-2"+n, 72, 112+float64(i)*20, 10),
			line(n, 300, 760, 10),
		}})
	}
	doc := &pdf.Document{
		Outline: []pdf.OutlineEntry{{Title: "References", Level: 1, Page: 1}},
		Pages:   pages,
	}
	got, err := pdf.Build(doc, "references.pdf")
	s.Require().NoError(err)
	text := bodyText(got.Blocks)["References"]
	s.Contains(text, "3:103-23")
	s.NotContains(text, "\n3\n", "the page number is furniture")
}

func (s *ReadingSuite) TestAnOutlineHeadingInTheRightColumnStartsWhereItIsRead() {
	// The right column's heading sits higher on the page than the left
	// column's last lines, which still belong to the section before it.
	lines := []pdf.Line{
		line("First", 50, 80, 10),
		line("left column text one", 50, 100, 10),
		line("left column text two", 50, 112, 10),
		line("Second", 310, 100, 10),
		line("right column text", 310, 112, 10),
	}
	doc := &pdf.Document{
		Outline: []pdf.OutlineEntry{
			{Title: "First", Level: 1, Page: 1},
			{Title: "Second", Level: 1, Page: 1},
		},
		Pages: []pdf.Page{{Lines: lines}},
	}
	got, err := pdf.Build(doc, "columns.pdf")
	s.Require().NoError(err)
	s.Equal(map[string]string{
		"First":  "left column text one\nleft column text two",
		"Second": "right column text",
	}, bodyText(got.Blocks))
}

// titledPages is a document with no outline, no contents and no section
// numbers: each page opens with a 14pt title, then body text.
func titledPages(first []pdf.Line) []pdf.Page {
	pages := []pdf.Page{{Lines: first}}
	for i := range 5 {
		pages = append(pages, pdf.Page{Lines: []pdf.Line{
			line("Chapter "+strconv.Itoa(i+2), 50, 60, 14),
			line("Body text of a chapter.", 50, 90, 10),
			line("More body text of it.", 50, 102, 10),
		}})
	}
	return pages
}

func (s *ReadingSuite) TestATitleInTheRightColumnStartsWhereItIsRead() {
	got, err := pdf.Build(&pdf.Document{Pages: titledPages([]pdf.Line{
		line("Opening", 50, 60, 14),
		line("left column text one", 50, 100, 10),
		line("left column text two", 50, 112, 10),
		line("Second", 310, 100, 14),
		line("right column text", 310, 118, 10),
	})}, "titled.pdf")
	s.Require().NoError(err)
	text := bodyText(got.Blocks)
	s.Equal("left column text one\nleft column text two", text["Opening"])
	s.Equal("right column text", text["Second"])
}

func (s *ReadingSuite) TestASubheadingInAnotherFontIsATitleOfItsOwn() {
	results := line("Results", 50, 60, 14)
	results.Font = "Sans-Bold"
	scale := line("Measured Scale", 50, 77, 14)
	scale.Font = "Sans-Italic"
	got, err := pdf.Build(&pdf.Document{Pages: titledPages([]pdf.Line{
		results, scale, line("Body text under the scale.", 50, 100, 10),
	})}, "subheading.pdf")
	s.Require().NoError(err)
	s.Equal("Body text under the scale.", bodyText(got.Blocks)["Measured Scale"])
}

func (s *ReadingSuite) TestATitleBesideItsNumberIsTheHeading() {
	// "3." and its title share a row as two lines, the title on the right.
	var pages []pdf.Page
	for i := range 5 {
		n := strconv.Itoa(i + 1)
		pages = append(pages, pdf.Page{Lines: []pdf.Line{
			line(n+".", 50, 60, 16),
			line("Topic "+n, 80, 60, 16),
			line("Body text of topic "+n+".", 50, 90, 10),
		}})
	}
	got, err := pdf.Build(&pdf.Document{Pages: pages}, "numbered.pdf")
	s.Require().NoError(err)
	s.Equal("Body text of topic 3.", bodyText(got.Blocks)["3. Topic 3"])
}
