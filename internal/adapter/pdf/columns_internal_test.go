package pdf

import (
	"strconv"
	"strings"
	"testing"

	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/structs"
	"github.com/stretchr/testify/suite"
)

// ColumnsSuite checks reading order: columns a column at a time, and
// everything that only looks like columns in the order it came.
type ColumnsSuite struct{ suite.Suite }

func TestColumns(t *testing.T) { suite.Run(t, new(ColumnsSuite)) }

// textRun is a 10pt run of font "F" whose top is top.
func textRun(text string, left, right, top float64) run {
	return run{
		font:   "F",
		size:   10,
		text:   text,
		top:    top,
		bottom: top + 10,
		left:   left,
		right:  right,
	}
}

// column sets n lines of running text from left to right, one every 12
// points from top, each named prefix and its row.
func column(prefix string, n int, left, right, top float64) []run {
	out := make([]run, n)
	for i := range out {
		out[i] = textRun(prefix+strconv.Itoa(i), left, right, top+float64(i)*12)
	}
	return out
}

func textsOf(lines []Line) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = ln.Text
	}
	return out
}

func (s *ColumnsSuite) TestReadsTwoColumnsOneAtATime() {
	runs := append(column("left", 6, 50, 290, 100), column("right", 6, 310, 550, 100)...)
	s.Equal([]string{
		"left0", "left1", "left2", "left3", "left4", "left5",
		"right0", "right1", "right2", "right3", "right4", "right5",
	}, textsOf(buildLines(runs)))
}

func (s *ColumnsSuite) TestALineAcrossTheGutterClosesTheColumnsAbove() {
	// A title over both columns, then a figure caption across them halfway
	// down: each band of columns is read before the line that follows it.
	runs := []run{textRun("title", 50, 550, 70)}
	runs = append(runs, column("a", 5, 50, 290, 100)...)
	runs = append(runs, column("b", 5, 310, 550, 100)...)
	runs = append(runs, textRun("caption", 50, 550, 170))
	runs = append(runs, column("c", 5, 50, 290, 190)...)
	runs = append(runs, column("d", 5, 310, 550, 190)...)
	s.Equal([]string{
		"title",
		"a0", "a1", "a2", "a3", "a4", "b0", "b1", "b2", "b3", "b4",
		"caption",
		"c0", "c1", "c2", "c3", "c4", "d0", "d1", "d2", "d3", "d4",
	}, textsOf(buildLines(runs)))
}

func (s *ColumnsSuite) TestAMarginColumnIsReadBeforeTheTextBesideIt() {
	runs := append(column("note", 6, 20, 80, 100), column("left", 8, 100, 300, 100)...)
	runs = append(runs, column("right", 8, 320, 520, 100)...)
	got := textsOf(buildLines(runs))
	s.Equal([]string{"note0", "note5", "left0", "left7", "right0", "right7"},
		[]string{got[0], got[5], got[6], got[13], got[14], got[21]})
}

func (s *ColumnsSuite) TestADefinitionTableKeepsItsRows() {
	// One term per row, a row apart, beside a description that wraps: the
	// term column is not running text, so each term stays with its row.
	var runs []run
	for i := range 6 {
		top := 100 + float64(i)*30
		runs = append(runs,
			textRun("term"+strconv.Itoa(i), 50, 140, top),
			textRun("says"+strconv.Itoa(i), 160, 550, top),
			textRun("more"+strconv.Itoa(i), 160, 500, top+12),
		)
	}
	got := textsOf(buildLines(runs))
	s.Equal([]string{"term0", "says0", "more0", "term1"}, got[:4])
}

func (s *ColumnsSuite) TestATableKeepsItsRows() {
	// Short cells leave most of their column empty.
	var runs []run
	for i := range 8 {
		top := 100 + float64(i)*12
		n := strconv.Itoa(i)
		runs = append(runs,
			textRun("name"+n, 50, 90, top),
			textRun("value"+n, 200, 230, top),
			textRun("note"+n, 400, 440, top),
		)
	}
	got := textsOf(buildLines(runs))
	s.Equal([]string{"name0", "value0", "note0", "name1"}, got[:4])
}

func (s *ColumnsSuite) TestOneColumnKeepsItsOrder() {
	s.Equal([]string{"only0", "only1", "only2", "only3", "only4", "only5"},
		textsOf(buildLines(column("only", 6, 50, 550, 100))))
}

func (s *ColumnsSuite) TestADiagramDoesNotHideTheColumnsBelowIt() {
	// Rows of small labels across the top of the page outnumber the lines
	// of text beside them, but not their characters.
	var runs []run
	for i := range 30 {
		left := 50 + float64(i%6)*80
		top := 20 + float64(i/6)*6
		runs = append(runs, run{
			font: "F", size: 5, text: "lbl" + strconv.Itoa(i),
			left: left, right: left + 40, top: top, bottom: top + 5,
		})
	}
	for _, side := range []struct {
		name        string
		left, right float64
	}{{"left", 50, 290}, {"right", 310, 550}} {
		for _, r := range column(side.name, 10, side.left, side.right, 100) {
			r.text += " is a line of running text"
			runs = append(runs, r)
		}
	}
	var text []string
	for _, t := range textsOf(buildLines(runs)) {
		if !strings.HasPrefix(t, "lbl") {
			text = append(text, strings.Fields(t)[0])
		}
	}
	s.Equal([]string{"left8", "left9", "right0", "right1"}, text[8:12])
}

func (s *ColumnsSuite) TestBlocksOneAboveTheOtherAreNotColumns() {
	// A block on the right, then one on the left below it: read top down.
	runs := append(column("upper", 6, 310, 550, 100), column("lower", 6, 50, 290, 200)...)
	got := textsOf(buildLines(runs))
	s.Equal([]string{"upper5", "lower0"}, got[5:7])
}

func (s *ColumnsSuite) TestPositionsAreMeasuredFromTheCropBox() {
	// The crop box starts 66 points above the media box's bottom edge, so
	// a line 20 points under its top sits at y 702 in user space.
	f := frameOf(structs.FPDF_FS_RECTF{Left: 40, Top: 722, Right: 571, Bottom: 66})
	rects := []*responses.GetPageTextStructuredRect{
		{
			Text:          "top line",
			PointPosition: responses.CharPosition{Left: 60, Top: 712, Right: 200, Bottom: 702},
		},
		{
			Text:          "printer's slug",
			PointPosition: responses.CharPosition{Left: 44, Top: 760, Right: 300, Bottom: 750},
		},
	}
	runs := toRuns(rects, f)
	s.Require().Len(runs, 1, "text above the crop box is off the page")
	s.Equal("top line", runs[0].text)
	s.InDelta(10, runs[0].top, 1e-9)
	s.InDelta(20, runs[0].left, 1e-9)
}
