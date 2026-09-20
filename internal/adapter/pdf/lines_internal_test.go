package pdf

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// LinesSuite checks the rules that build MuPDF-shaped lines from PDFium's
// runs, each against the failure it was written for.
type LinesSuite struct{ suite.Suite }

func TestLines(t *testing.T) { suite.Run(t, new(LinesSuite)) }

// word is a run of font "F" at 10pt on the row from 100 to 110.
func word(text string, left, right float64) run {
	return run{font: "F", size: 10, text: text, top: 100, bottom: 110, left: left, right: right}
}

func (s *LinesSuite) TestSameFontGlyphsJoinWithoutSpaces() {
	// A footer set one glyph per run must read "Phone", not "P h o n e".
	runs := []run{
		word("P", 0, 5),
		word("h", 5.5, 10),
		word("o", 10.5, 15),
		word("n", 15.5, 20),
		word("e", 20.5, 25),
	}
	lines := buildLines(runs)
	s.Require().Len(lines, 1)
	s.Equal("Phone", lines[0].Text)
}

func (s *LinesSuite) TestAWordGapGetsASpace() {
	lines := buildLines([]run{word("two", 0, 15), word("words", 17, 40)})
	s.Require().Len(lines, 1)
	s.Equal("two words", lines[0].Text)
}

func (s *LinesSuite) TestAFontChangeGetsASpace() {
	bold := word("Bold", 0, 20)
	bold.weight = 700
	lines := buildLines([]run{bold, word("plain", 20, 40)})
	s.Require().Len(lines, 1)
	s.Equal("Bold plain", lines[0].Text)
}

func (s *LinesSuite) TestACellGapSplitsSameFontRuns() {
	// A contents row: "2.2." then its title a little under half an em away.
	lines := buildLines([]run{word("2.2.", 0, 15), word("Setup", 20, 45)})
	s.Require().Len(lines, 2)
	s.Equal([]string{"2.2.", "Setup"}, []string{lines[0].Text, lines[1].Text})
}

func (s *LinesSuite) TestSameSizeLinesInARowShareOneExtent() {
	// PDFium's boxes hug the glyphs; the contents parser needs one top per row.
	number := word("2.2.", 0, 15)
	title := word("Setup", 20, 45)
	title.top, title.bottom = 101, 111
	lines := buildLines([]run{number, title})
	s.Require().Len(lines, 2)
	s.InDelta(100, lines[0].Y0, 0)
	s.InDelta(100, lines[1].Y0, 0)
}

func (s *LinesSuite) TestVerticalTextIsALineOfItsOwn() {
	// A margin watermark overlaps every row and must not join any.
	watermark := run{
		font:   "W",
		size:   10,
		text:   "Author Manuscript",
		top:    50,
		bottom: 500,
		left:   10,
		right:  20,
	}
	lines := buildLines([]run{word("body", 100, 120), watermark})
	s.Require().Len(lines, 2)
	s.ElementsMatch([]string{"body", "Author Manuscript"}, []string{lines[0].Text, lines[1].Text})
}

func (s *LinesSuite) TestPageTextReadsAsMuPDFWritesIt() {
	s.Equal("one\ntwo\n", pageText("one\r\ntwo"))
	s.Empty(pageText(""))
}

func (s *LinesSuite) TestContentsRowSplitsANumberMergedWithItsTitle() {
	e, ok := contentsRow([]string{"7.17. Deep Section", "212"})
	s.Require().True(ok)
	s.Equal(tocEntry{section: "7.17", title: "Deep Section", page: 212}, e)

	_, ok = contentsRow([]string{"7.17.", "Deep Section", "ii"})
	s.False(ok, "a roman page number is not a contents row")
}
