package pdf

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// Thresholds for building lines from runs, in multiples of the font size.
// Each fixed a measured failure; docs/research/pdfium-spike.md has them.
const (
	// lineGap splits two runs in a row into separate lines: a column break.
	lineGap = 1.0
	// cellGap splits two same-font runs. PDFium already merges continuous
	// same-font text, so two side by side are apart only because the text
	// jumped to a tab stop or a table cell. It sits between a glyph-spaced
	// footer's word gaps, about 0.35em, and a contents row's gap after a long
	// section number, about 0.48em.
	cellGap = 0.45
	// wordGap is the gap above which two runs on one line are separate words;
	// a word space is roughly a quarter em.
	wordGap = 0.15
	// bandOverlap is the share of the shorter run's height two runs must
	// overlap by to sit in one row.
	bandOverlap = 0.5
)

// run is one stretch of same-font text, as PDFium reports it, in points
// from the page's top left.
type run struct {
	font                     string
	text                     string
	top, left, bottom, right float64
	// size is the rendered font size, after the text matrix: some producers
	// set text at a nominal size of 1 and scale it, and nominal sizes then
	// show no hierarchy at all.
	size   float64
	weight int
	// spaceBefore and spaceAfter record whitespace PDFium put at that edge.
	spaceBefore, spaceAfter bool
}

func (r run) sameFont(o run) bool {
	return r.font == o.font && r.weight == o.weight && r.size == o.size
}

// em is the run's font size, at least one point, the unit the gaps are
// measured in.
func (r run) em() float64 { return math.Max(r.size, 1) }

// buildLines groups a page's runs into lines the way MuPDF groups spans, and
// returns them sorted as the adapter reads them.
func buildLines(runs []run) []Line {
	horizontal, vertical := splitVertical(runs)
	var out []Line
	for _, band := range rows(horizontal) {
		out = append(out, bandLines(band)...)
	}
	for _, r := range vertical {
		out = append(out, lineOf([]run{r}, r.top, r.bottom))
	}
	return sortedLines(out)
}

// splitVertical separates vertical text, such as an "Author Manuscript"
// watermark down the margin of some journal PDFs, which overlaps every row
// on the page and would glue itself onto each. MuPDF reports it as a line of
// its own.
func splitVertical(runs []run) ([]run, []run) {
	var horizontal, vertical []run
	for _, r := range runs {
		if utf8.RuneCountInString(r.text) > 2 && r.bottom-r.top > 2*(r.right-r.left) {
			vertical = append(vertical, r)
			continue
		}
		horizontal = append(horizontal, r)
	}
	return horizontal, vertical
}

// rows groups runs whose vertical extents overlap, top to bottom.
func rows(runs []run) [][]run {
	sorted := slices.Clone(runs)
	slices.SortStableFunc(sorted, func(a, b run) int { return cmp.Compare(a.top, b.top) })
	var out [][]run
	var top, bottom float64
	for _, r := range sorted {
		if n := len(out); n > 0 {
			h := math.Min(bottom-top, r.bottom-r.top)
			overlap := math.Min(bottom, r.bottom) - math.Max(top, r.top)
			if h > 0 && overlap >= bandOverlap*h {
				out[n-1] = append(out[n-1], r)
				top, bottom = math.Min(top, r.top), math.Max(bottom, r.bottom)
				continue
			}
		}
		out = append(out, []run{r})
		top, bottom = r.top, r.bottom
	}
	return out
}

// bandLines splits one row into lines, left to right, wherever the gap is a
// column break.
//
// Lines in a row that share a font size take one extent. PDFium's boxes hug
// the glyphs, so "2.2." and its title on one baseline get tops a point
// apart; MuPDF derives a line's box from the font's metrics, so both get one
// box, and the contents parser groups rows by that top. Only same-size lines
// share: a 12pt heading in one column and 9pt table text in the other keep
// their own, since unnumbered headings are keyed by their top.
func bandLines(band []run) []Line {
	extents := map[float64][2]float64{}
	for _, r := range band {
		sz := pystr.Round(r.size, 1)
		e, ok := extents[sz]
		if !ok {
			e = [2]float64{r.top, r.bottom}
		}
		extents[sz] = [2]float64{math.Min(e[0], r.top), math.Max(e[1], r.bottom)}
	}
	row := slices.Clone(band)
	slices.SortStableFunc(row, func(a, b run) int { return cmp.Compare(a.left, b.left) })
	var out []Line
	start := 0
	for i := 1; i <= len(row); i++ {
		if i < len(row) && !splits(row[i-1], row[i]) {
			continue
		}
		e := extents[pystr.Round(row[start].size, 1)]
		out = append(out, lineOf(row[start:i], e[0], e[1]))
		start = i
	}
	return out
}

// splits reports whether cur starts a new line after prev: a gap wider than
// an em, or wider than cellGap between two same-font runs.
func splits(prev, cur run) bool {
	gap := cur.left - prev.right
	return gap > lineGap*prev.em() || (prev.sameFont(cur) && gap > cellGap*prev.em())
}

// lineOf joins runs into one line. A change of font or size always gets a
// space, as MuPDF starts a span at every font change and the adapter joins
// spans with one. Same-font runs are letter-spaced text arriving a glyph at
// a time and join without a space unless the gap is a word space, or
// "Phone" would read "P h o n e".
func lineOf(runs []run, top, bottom float64) Line {
	if len(runs) == 0 {
		return Line{}
	}
	first := runs[0]
	var b strings.Builder
	b.WriteString(first.text)
	prev := first
	for _, r := range runs[1:] {
		if !prev.sameFont(r) || r.left-prev.right > wordGap*prev.em() || prev.spaceAfter ||
			r.spaceBefore {
			b.WriteByte(' ')
		}
		b.WriteString(r.text)
		prev = r
	}
	return Line{
		Text: pystr.Strip(b.String()),
		Y0:   top,
		X0:   first.left,
		Y1:   bottom,
		Size: pystr.Round(first.size, 1),
	}
}

// sortedLines drops empty lines and orders the rest by top, rounded to a
// tenth of a point, then left, as the Python adapter orders MuPDF's.
func sortedLines(lines []Line) []Line {
	out := slices.DeleteFunc(lines, func(ln Line) bool { return ln.Text == "" })
	slices.SortStableFunc(out, func(a, b Line) int {
		return cmp.Or(
			cmp.Compare(pystr.Round(a.Y0, 1), pystr.Round(b.Y0, 1)),
			cmp.Compare(a.X0, b.X0),
		)
	})
	return out
}
