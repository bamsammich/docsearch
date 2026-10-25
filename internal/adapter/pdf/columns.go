package pdf

import (
	"cmp"
	"math"
	"slices"
	"unicode/utf8"
)

// Thresholds for reading a page in columns.
const (
	// minColumnLines is the fewest lines each side of a gutter needs before
	// the two sides are columns rather than a stray label beside the text.
	minColumnLines = 5
	// minGutter is the narrowest gap between columns, in multiples of the
	// page's text size. bandLines already splits a row only at a gap
	// wider than lineGap, so columns sit at least that far apart.
	minGutter = 0.8
	// maxCrossingShare is the largest share of a page's lines that may
	// cross a gutter: a title, an abstract or a figure set across both
	// columns.
	maxCrossingShare = 1.0 / 3
	// minSideBySide is how much of the shorter column's height the other
	// column must run alongside. Two blocks one above the other are not
	// columns.
	minSideBySide = 0.5
	// maxLeading is the farthest, in multiples of its font size, a line of
	// running text sits below the one before it. A column of text runs line
	// after line; the terms of a definition table sit one to a row, a row
	// apart.
	maxLeading = 1.5
	// minTextSize is the smallest font size, as a share of the page's text
	// size, that counts toward finding columns. The labels of a diagram
	// are set far smaller than the text around it, and a page of them
	// would hide the columns below.
	minTextSize = 0.8
	// maxColumnDepth is how many times a column is itself split, for a
	// margin column beside two columns of text.
	maxColumnDepth = 2
	// minColumnFill is how much of its column's width the median line must
	// span. Running text fills its column; the cells of a table leave
	// most of theirs empty, and reading a table a column at a time would
	// part every value from its label.
	minColumnFill = 0.6
)

// readingOrder orders a page's lines, sorted by top then left, for reading.
// A page set in columns is read a column at a time: sorted by top alone, its
// columns interleave row by row, and every paragraph breaks off mid-sentence
// into the one beside it.
//
// A page without a gutter keeps the order it came in.
func readingOrder(lines []laidLine) []Line {
	ordered := columns(lines, maxColumnDepth)
	out := make([]Line, len(ordered))
	for i, ln := range ordered {
		out[i] = ln.Line
	}
	return out
}

// columns reads lines left column first when a gutter divides them. A line
// that crosses the gutter, such as a heading set across both columns, closes
// the columns above it: those are read before it, and the columns below it
// after.
func columns(lines []laidLine, depth int) []laidLine {
	if depth == 0 {
		return lines
	}
	mid, ok := findGutter(lines)
	if !ok {
		return lines
	}
	out := make([]laidLine, 0, len(lines))
	var left, right []laidLine
	flush := func() {
		out = append(out, columns(left, depth-1)...)
		out = append(out, columns(right, depth-1)...)
		left, right = nil, nil
	}
	for _, ln := range lines {
		switch side(ln, mid) {
		case sideLeft:
			left = append(left, ln)
		case sideRight:
			right = append(right, ln)
		default:
			flush()
			out = append(out, ln)
		}
	}
	flush()
	return out
}

type gutterSide int

const (
	sideCrossing gutterSide = iota
	sideLeft
	sideRight
)

// side is which side of the gutter at mid a line lies wholly on.
func side(ln laidLine, mid float64) gutterSide {
	switch {
	case ln.right <= mid:
		return sideLeft
	case ln.X0 >= mid:
		return sideRight
	}
	return sideCrossing
}

// findGutter finds the vertical gap between two columns of text and returns
// its middle.
//
// It looks first for a gap no line crosses, then for one a single line
// crosses, and so on up to maxCrossingShare of the lines: a column gutter
// is clear but for the few lines set across it. Of the gaps that pass
// isGutter, it takes the one dividing the lines most evenly.
func findGutter(all []laidLine) (float64, bool) {
	lines := textLines(all)
	if len(lines) < 2*minColumnLines {
		return 0, false
	}
	cover, left := coverage(lines)
	width := minGutter * textSize(lines)
	for crossing := 0; crossing <= int(float64(len(lines))*maxCrossingShare); crossing++ {
		if mid, ok := evenestGutter(lines, cover, left, gaps(cover, crossing), width); ok {
			return mid, true
		}
	}
	return 0, false
}

// evenestGutter is the middle of the gap at least width wide that divides
// the lines into columns most evenly, measured from left.
func evenestGutter(lines []laidLine, cover []int, left float64, candidates []gap,
	width float64,
) (float64, bool) {
	best, bestBalance := 0.0, 0
	for _, g := range candidates {
		if float64(g.end-g.start) < width {
			continue
		}
		mid := left + g.middle(cover)
		if balance, ok := isGutter(lines, mid); ok && balance > bestBalance {
			best, bestBalance = mid, balance
		}
	}
	return best, bestBalance > 0
}

// textLines are the lines that say where the columns are: set across the
// page, and not much smaller than most of its text.
func textLines(lines []laidLine) []laidLine {
	across := slices.DeleteFunc(slices.Clone(lines), func(ln laidLine) bool { return ln.vertical })
	if len(across) == 0 {
		return across
	}
	least := minTextSize * textSize(across)
	return slices.DeleteFunc(across, func(ln laidLine) bool { return ln.Size < least })
}

// coverage counts the lines over each point of the page's width, from the
// leftmost line's left edge, and returns the counts and that edge.
func coverage(lines []laidLine) ([]int, float64) {
	left, right := math.Inf(1), math.Inf(-1)
	for _, ln := range lines {
		left, right = math.Min(left, ln.X0), math.Max(right, ln.right)
	}
	cover := make([]int, int(math.Ceil(right-left)))
	for _, ln := range lines {
		from := max(0, int(math.Floor(ln.X0-left)))
		to := min(len(cover), int(math.Ceil(ln.right-left)))
		for x := from; x < to; x++ {
			cover[x]++
		}
	}
	return cover, left
}

// gap is a stretch of points, start inclusive and end exclusive.
type gap struct {
	start, end int
}

// gaps lists the stretches no more than crossing lines cover, leaving out
// those at either edge of the text.
func gaps(cover []int, crossing int) []gap {
	all := stretches(
		cover,
		gap{start: 0, end: len(cover)},
		func(n int) bool { return n <= crossing },
	)
	return slices.DeleteFunc(all, func(g gap) bool { return g.start == 0 || g.end == len(cover) })
}

// middle is the centre of the widest stretch of the gap that the fewest
// lines cover. A gap reached by a ragged line end or two would otherwise
// have its middle pulled toward them.
func (g gap) middle(cover []int) float64 {
	least := slices.Min(cover[g.start:g.end])
	widest := slices.MaxFunc(
		stretches(cover, g, func(n int) bool { return n == least }),
		func(a, b gap) int { return cmp.Compare(a.end-a.start, b.end-b.start) },
	)
	return float64(widest.start+widest.end) / 2
}

// stretches lists the longest runs of points within g whose count keep
// accepts.
func stretches(cover []int, g gap, keep func(int) bool) []gap {
	var out []gap
	start := -1
	for x := g.start; x <= g.end; x++ {
		inside := x < g.end && keep(cover[x])
		switch {
		case inside && start < 0:
			start = x
		case !inside && start >= 0:
			out = append(out, gap{start: start, end: x})
			start = -1
		}
	}
	return out
}

// isGutter reports whether a gap at mid divides the lines into columns,
// and how evenly: the line count of the smaller side.
func isGutter(lines []laidLine, mid float64) (int, bool) {
	var left, right []laidLine
	crossing := 0
	for _, ln := range lines {
		switch side(ln, mid) {
		case sideLeft:
			left = append(left, ln)
		case sideRight:
			right = append(right, ln)
		default:
			crossing++
		}
	}
	if len(left) < minColumnLines || len(right) < minColumnLines ||
		float64(crossing) > float64(len(lines))*maxCrossingShare {
		return 0, false
	}
	return min(len(left), len(right)),
		sideBySide(left, right) && isText(left) && isText(right)
}

// isText reports whether lines on one side of a gutter read as a column of
// running text: lines that fill its width, one under the next.
func isText(lines []laidLine) bool {
	return fillsColumn(lines) && runsOn(lines)
}

// runsOn reports whether most lines, taken top to bottom, start within
// maxLeading of the line before.
func runsOn(lines []laidLine) bool {
	sorted := slices.Clone(lines)
	slices.SortStableFunc(sorted, func(a, b laidLine) int { return cmpFloat(a.Y0, b.Y0) })
	near := 0
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Y0-sorted[i-1].Y0 <= maxLeading*sorted[i-1].Size {
			near++
		}
	}
	return 2*near >= len(sorted)-1
}

// fillsColumn reports whether the median line spans most of the width the
// lines take together.
func fillsColumn(lines []laidLine) bool {
	left, right := math.Inf(1), math.Inf(-1)
	widths := make([]float64, len(lines))
	for i, ln := range lines {
		left, right = math.Min(left, ln.X0), math.Max(right, ln.right)
		widths[i] = ln.right - ln.X0
	}
	slices.Sort(widths)
	return widths[len(widths)/2] >= minColumnFill*(right-left)
}

// sideBySide reports whether two sets of lines run alongside each other for
// most of the shorter one's height.
func sideBySide(a, b []laidLine) bool {
	aTop, aBottom := extent(a)
	bTop, bBottom := extent(b)
	overlap := math.Min(aBottom, bBottom) - math.Max(aTop, bTop)
	shorter := math.Min(aBottom-aTop, bBottom-bTop)
	return shorter > 0 && overlap >= minSideBySide*shorter
}

// extent is the top of the highest line and the bottom of the lowest.
func extent(lines []laidLine) (float64, float64) {
	top, bottom := math.Inf(1), math.Inf(-1)
	for _, ln := range lines {
		top, bottom = math.Min(top, ln.Y0), math.Max(bottom, ln.Y1)
	}
	return top, bottom
}

// textSize is the font size carrying the most characters, the size of the
// page's running text. A diagram's labels can outnumber its lines, but not
// its characters.
func textSize(lines []laidLine) float64 {
	chars := map[float64]int{}
	best := lines[0].Size
	for _, ln := range lines {
		chars[ln.Size] += utf8.RuneCountInString(ln.Text)
		if chars[ln.Size] > chars[best] || (chars[ln.Size] == chars[best] && ln.Size > best) {
			best = ln.Size
		}
	}
	return best
}
