package pdf

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// titleJoinGap is how far below a title line, as a share of its size, the
// next line may start and still be the same title wrapped. A title set over
// two lines sits at its own leading, about half its size apart; the next
// title is paragraphs away.
const titleJoinGap = 0.75

// besideOffset is how far to one side of a title line, as a multiple of its
// size, a line at another size must start to be set in another column
// rather than under the title. Anything set in a margin level with a long
// title, such as a side note, a running label or a journal's masthead,
// sorts between the title's lines, since lines are read top to bottom.
const besideOffset = 10

// titlePlacements reads a document whose headings carry no numbers at all.
//
// A manual titled in capitals with no outline, no contents to reconstruct
// and no section numbers has only one structure to offer: its titles. The
// font heuristic would otherwise take them as subdivision points inside
// sections it never finds, and leave every chunk without a heading.
//
// The title size is the heading size that heads the most pages, since a
// chapter title recurs through the book where a caption or a label does
// not. Larger sizes are titles too, at the same level: front matter set
// larger than the chapters still names what follows it. Each title is a
// section numbered in document order, a number the document never prints,
// so the heading path carries the title alone. It returns the placements and
// the titles by section, or none when no line is at a title size.
func titlePlacements(
	pages []Page,
	headingSizes []float64,
	boiler boilerplate,
) ([]placement, map[string]string) {
	size, ok := titleSize(pages, headingSizes, boiler)
	if !ok {
		return nil, nil
	}
	isTitle := func(ln Line) bool {
		return ln.Size >= size && slices.Contains(headingSizes, ln.Size) &&
			pystr.Strip(ln.Text) != "" && !boiler.matches(ln.Text)
	}
	var placements []placement
	titles := map[string]string{}
	for pno, p := range pages {
		placements = pageTitles(placements, titles, p.Lines, pno, isTitle)
	}
	return placements, titles
}

// pageTitles appends the titles on one page to placements, numbering each
// in document order and recording it in titles. A line that carries on a
// title already placed is not a title of its own.
func pageTitles(
	placements []placement,
	titles map[string]string,
	lines []Line,
	pno int,
	isTitle func(Line) bool,
) []placement {
	taken := map[int]bool{}
	for i, ln := range lines {
		if taken[i] || !isTitle(ln) {
			continue
		}
		title, rest := joinTitle(lines, i)
		section := strconv.Itoa(len(placements) + 1)
		placements = append(placements, placement{
			section: section, title: title, page: pno, line: i,
			rest: rest, matched: true,
		})
		titles[section] = title
		for _, k := range rest {
			taken[k] = true
		}
	}
	return placements
}

// titleSize is the heading size whose unnumbered lines head the most pages,
// the larger on a tie.
func titleSize(pages []Page, headingSizes []float64, boiler boilerplate) (float64, bool) {
	pagesAt := pagesPerSize(pages, headingSizes, boiler)
	best, found := 0.0, false
	for size, count := range pagesAt {
		if !found || count > pagesAt[best] || (count == pagesAt[best] && size > best) {
			best, found = size, true
		}
	}
	return best, found
}

// pagesPerSize counts, for each heading size, the pages holding an
// unnumbered line at it.
func pagesPerSize(pages []Page, headingSizes []float64, boiler boilerplate) map[float64]int {
	pagesAt := map[float64]int{}
	for _, p := range pages {
		seen := map[float64]bool{}
		for _, ln := range p.Lines {
			if seen[ln.Size] || !unnumberedHeading(ln, headingSizes, boiler) {
				continue
			}
			seen[ln.Size] = true
			pagesAt[ln.Size]++
		}
	}
	return pagesAt
}

// unnumberedHeading reports whether a line is at a heading size, carries no
// section number, and is not page furniture.
func unnumberedHeading(ln Line, headingSizes []float64, boiler boilerplate) bool {
	return slices.Contains(headingSizes, ln.Size) && !boiler.matches(ln.Text) &&
		!sectionLine.MatchString(ln.Text)
}

// joinTitle is the title starting at lines[i], with any lines that carry it
// on in the same font and size just below, and the indices of those lines. A
// subheading set right under its heading, at the heading's size but in
// italic, is a title of its own. A line set beside the title in another
// column is stepped over rather than ending it, and stays text.
func joinTitle(lines []Line, i int) (string, []int) {
	parts := []string{pystr.Strip(lines[i].Text)}
	var rest []int
	last := lines[i]
	for k := i + 1; k < len(lines); k++ {
		switch {
		case carriesOn(last, lines[k]):
			parts = append(parts, pystr.Strip(lines[k].Text))
			rest = append(rest, k)
			last = lines[k]
		case beside(last, lines[k]):
			continue
		default:
			return strings.Join(parts, " "), rest
		}
	}
	return strings.Join(parts, " "), rest
}

// beside reports whether ln is set in another column from the title line
// last: at another size, and starting well to one side of it.
func beside(last, ln Line) bool {
	return ln.Size != last.Size && math.Abs(ln.X0-last.X0) > last.Size*besideOffset
}

// carriesOn reports whether next carries on the title line last: set in the
// same font and size, just below it.
func carriesOn(last, next Line) bool {
	return next.Size == last.Size && next.Font == last.Font &&
		next.Y0 > last.Y0 && next.Y0-last.Y1 <= last.Size*titleJoinGap
}
