package pdf

import (
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
		for i := 0; i < len(p.Lines); i++ {
			if !isTitle(p.Lines[i]) {
				continue
			}
			title, wrapped := joinTitle(p.Lines, i)
			section := strconv.Itoa(len(placements) + 1)
			placements = append(placements, placement{
				section: section, title: title, page: pno, y: p.Lines[i].Y0,
				wrapped: wrapped, matched: true,
			})
			titles[section] = title
			i += wrapped
		}
	}
	return placements, titles
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
// on at the same size just below, and how many lines it took past the first.
func joinTitle(lines []Line, i int) (string, int) {
	parts := []string{pystr.Strip(lines[i].Text)}
	j := i
	for j+1 < len(lines) {
		next, last := lines[j+1], lines[j]
		if next.Size != last.Size || next.Y0-last.Y1 > last.Size*titleJoinGap {
			break
		}
		parts = append(parts, pystr.Strip(next.Text))
		j++
	}
	return strings.Join(parts, " "), j - i
}
