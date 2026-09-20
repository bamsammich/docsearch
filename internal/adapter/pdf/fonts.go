package pdf

import (
	"slices"
	"unicode/utf8"
)

const (
	// headingSizeRatio is how much larger than body text a size must be to
	// be a heading level.
	headingSizeRatio = 1.10
	// boilerplatePageFraction is the share of pages a line must repeat on,
	// digits normalized, to be furniture. Running furniture hits ~100%; real
	// prose never does.
	boilerplatePageFraction = 0.25
	// boilerplateMinPages is the page count below which repetition is not
	// evidence of furniture and the false-positive risk dominates.
	boilerplateMinPages = 8
)

// sizeCount counts per font size, remembering the order sizes were first
// seen: Python's Counter breaks a tie for most common by that order.
type sizeCount struct {
	counts map[float64]int
	order  []float64
}

func newSizeCount() *sizeCount { return &sizeCount{counts: map[float64]int{}} }

func (c *sizeCount) add(size float64, n int) {
	if _, ok := c.counts[size]; !ok {
		c.order = append(c.order, size)
	}
	c.counts[size] += n
}

// mostCommon is the size with the highest count, the first seen on a tie.
func (c *sizeCount) mostCommon() float64 {
	best := c.order[0]
	for _, size := range c.order[1:] {
		if c.counts[size] > c.counts[best] {
			best = size
		}
	}
	return best
}

// atLeast lists the sizes at least size, in the order first seen.
func (c *sizeCount) atLeast(size float64) []float64 {
	var out []float64
	for _, s := range c.order {
		if s >= size {
			out = append(out, s)
		}
	}
	return out
}

// analyzeFonts returns the body text size, the size carrying the most
// characters, and the heading sizes, largest first so that a size's index is
// its nesting depth.
func analyzeFonts(pages []Page) (float64, []float64) {
	volume, linesAt := newSizeCount(), newSizeCount()
	for _, p := range pages {
		for _, ln := range p.Lines {
			volume.add(ln.Size, utf8.RuneCountInString(ln.Text))
			linesAt.add(ln.Size, 1)
		}
	}
	if len(volume.order) == 0 {
		return 0, []float64{}
	}
	body := volume.mostCommon()
	// Enough lines to be a real level, so two stray lines of spillover do
	// not become one.
	heads := headingSizesOf(linesAt, body, max(5, len(pages)/400))
	slices.SortStableFunc(heads, func(a, b float64) int { return cmpFloat(b, a) })
	return body, heads
}

// headingSizesOf picks the sizes large enough over body, and common enough,
// to be heading levels.
func headingSizesOf(linesAt *sizeCount, body float64, floor int) []float64 {
	var established []float64
	for _, size := range linesAt.order {
		if size >= body*headingSizeRatio && linesAt.counts[size] >= floor {
			established = append(established, size)
		}
	}
	if len(established) == 0 {
		return []float64{}
	}
	// Anything at least as large as an established level is a heading
	// however rare. A lone chapter heading set larger than every other fails
	// the floor, and dropping it makes that chapter's content annex the
	// section before it. The floor rejects body-sized noise, not oversized
	// rarities.
	return linesAt.atLeast(slices.Min(established))
}

// detectBoilerplate finds running headers and footers by how often they
// repeat, not where they sit: some producers emit the page-bottom copyright
// line first in reading order, so a top and bottom band test misses it.
//
// Heading-sized lines are left out. Digits normalize to '#', so "1.", "2."
// and "3." are one key, and on a short document that collision alone clears
// the threshold and would strip every chapter heading. Running furniture is
// set small; a heading-sized line never is.
func detectBoilerplate(pages []Page, headingSizes []float64) boilerplate {
	found := boilerplate{}
	if len(pages) < boilerplateMinPages {
		return found
	}
	threshold := max(3, int(float64(len(pages))*boilerplatePageFraction))
	for key, onPages := range pagesPerLine(pages, headingSizes) {
		if len(onPages) >= threshold {
			found[key] = true
		}
	}
	return found
}

// pagesPerLine maps each normalized line below heading size to the pages it
// appears on.
func pagesPerLine(pages []Page, headingSizes []float64) map[string]map[int]bool {
	seen := map[string]map[int]bool{}
	for i, p := range pages {
		for _, ln := range p.Lines {
			if !slices.Contains(headingSizes, ln.Size) {
				addPage(seen, normalize(ln.Text), i)
			}
		}
	}
	return seen
}

func addPage(seen map[string]map[int]bool, key string, page int) {
	if seen[key] == nil {
		seen[key] = map[int]bool{}
	}
	seen[key][page] = true
}
