package pdf_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/domain"
)

// TitlesSuite covers a document with no outline, no printed contents and
// no section numbers: a guide titled in capitals, whose only numbered lines
// are a list of instructions set larger than the body.
type TitlesSuite struct{ suite.Suite }

func TestTitles(t *testing.T) { suite.Run(t, new(TitlesSuite)) }

const (
	bodySize  = 8
	listSize  = 9
	titleSize = 12
)

// page lays lines out down a page, one every 14 points, unless a line's Y0
// is already set.
func page(lines ...pdf.Line) pdf.Page {
	y := 20.0
	for i := range lines {
		if lines[i].Y0 == 0 {
			lines[i].Y0 = y
		}
		lines[i].Y1 = lines[i].Y0 + lines[i].Size*0.75
		lines[i].X0 = 24
		y = lines[i].Y0 + 14
	}
	return pdf.Page{Lines: lines}
}

func body(text string) pdf.Line { return pdf.Line{Text: text, Size: bodySize} }

func title(text string) pdf.Line { return pdf.Line{Text: text, Size: titleSize} }

// prose is enough body text to make bodySize the body.
func prose(n int) []pdf.Line {
	out := make([]pdf.Line, n)
	for i := range out {
		out[i] = body("Slide the cover open until it clicks into place, then lift the tray " +
			"and remove the packing insert " + strconv.Itoa(i) + ".")
	}
	return out
}

// safetyList is a numbered list of instructions, each a sentence, at a size
// above the body.
func safetyList() pdf.Page {
	lines := []pdf.Line{title("IMPORTANT SAFETY INSTRUCTIONS")}
	for i, item := range []string{
		"Do not let children operate the unit. Supervise its use near them.",
		"Use the unit only for the purpose this guide describes.",
		"Never run the unit with a damaged cable.",
		"Never push anything into the vents.",
		"Do not use outdoors.",
		"Keep hands clear of the rollers.",
	} {
		lines = append(lines, pdf.Line{Text: strconv.Itoa(i+1) + ". " + item, Size: listSize})
	}
	return page(lines...)
}

// chapter is a page headed by a title, set over the lines given.
func chapter(titleLines ...string) pdf.Page {
	lines := []pdf.Line{}
	for i, t := range titleLines {
		ln := title(t)
		// A wrapped title sits at its own leading, closer than the body's.
		ln.Y0 = 15 + float64(i)*14.4
		lines = append(lines, ln)
	}
	rest := prose(6)
	for i := range rest {
		rest[i].Y0 = 60 + float64(i)*14
	}
	return page(append(lines, rest...)...)
}

func (s *TitlesSuite) build(pages ...pdf.Page) *domain.Extraction {
	got, err := pdf.Build(&pdf.Document{Pages: pages}, "manual.pdf")
	s.Require().NoError(err)
	return got
}

// headingPaths is every block's heading path, joined.
func headingPaths(x *domain.Extraction) []string {
	out := []string{}
	for _, b := range x.Blocks {
		out = append(out, strings.Join(b.HeadingPath, " > "))
	}
	return out
}

// The numbered instructions are a list, not eighteen sections; and with no
// numbering left, the titles are the sections.
func (s *TitlesSuite) TestANumberedListOfSentencesIsNotSectionNumbering() {
	got := s.build(
		safetyList(),
		chapter("PARTS OF THE UNIT"),
		chapter("ACCESSORIES"),
		chapter("LOADING THE TAPE"),
		chapter("CLEANING THE HEAD"),
		chapter("TROUBLESHOOTING"),
	)
	paths := headingPaths(got)
	for _, p := range paths {
		s.NotRegexp(`^\d+\. `, p, "a list item became a section")
	}
	s.Equal([]string{
		"IMPORTANT SAFETY INSTRUCTIONS", "PARTS OF THE UNIT", "ACCESSORIES",
		"LOADING THE TAPE", "CLEANING THE HEAD", "TROUBLESHOOTING",
	}, paths)
	s.Contains(got.Blocks[0].Text, "4. Never push anything into the vents",
		"the list is the text of the section it sits in")
	s.Equal([]int{1}, got.Diagnostics["numbered_list_pages"])
	s.Equal("unnumbered_titles", got.Diagnostics["headings_from"])
}

// A title set over two lines is one heading, and neither line is text.
func (s *TitlesSuite) TestAWrappedTitleIsOneHeading() {
	got := s.build(
		chapter("REPLACING THE INK", "CARTRIDGE"),
		chapter("PARTS OF THE UNIT"),
		chapter("ACCESSORIES"),
		chapter("CLEANING THE HEAD"),
		chapter("TROUBLESHOOTING"),
	)
	s.Equal("REPLACING THE INK CARTRIDGE", headingPaths(got)[0])
	s.NotContains(got.Blocks[0].Text, "CARTRIDGE")
}

// A document that numbers its sections keeps its numbering: the titles are
// a fallback for one that has none.
func (s *TitlesSuite) TestNumberedHeadingsStillWin() {
	got := s.build(
		chapter("1. Setup"),
		chapter("2. Loading"),
		chapter("3. Printing"),
		chapter("4. Care"),
		chapter("5. Troubleshooting"),
	)
	s.Equal([]string{
		"1. Setup", "2. Loading", "3. Printing", "4. Care", "5. Troubleshooting",
	}, headingPaths(got))
	s.Nil(got.Diagnostics["headings_from"])
}

// Short numbered titles on one page are a run of sections, not a list:
// only sentences give a list away.
func (s *TitlesSuite) TestShortNumberedTitlesOnOnePageAreStillSections() {
	short := page(append([]pdf.Line{
		title("1. Setup"), body("Unpack the unit."),
		title("2. Loading"), body("Load the tape."),
		title("3. Printing"), body("Print a label."),
	}, prose(4)...)...)
	got := s.build(short, chapter("4. Care"), chapter("5. Troubleshooting"))
	s.Equal("1. Setup", headingPaths(got)[0])
	s.Nil(got.Diagnostics["numbered_list_pages"])
}
