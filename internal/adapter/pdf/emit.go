package pdf

import (
	"slices"
	"strings"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
)

// blockBuffer gathers the lines of the block being built.
type blockBuffer struct {
	lines []string
	// page and pageEnd are 1-based; page is 0 until a line is added.
	page, pageEnd int
	images        int
	subdivision   bool
}

func (b *blockBuffer) add(text string, pno int) {
	b.lines = append(b.lines, text)
	if b.page == 0 {
		b.page = pno + 1
	}
	b.pageEnd = pno + 1
}

// text joins the non-blank lines.
func (b *blockBuffer) text() string {
	kept := make([]string, 0, len(b.lines))
	for _, ln := range b.lines {
		if pystr.Strip(ln) != "" {
			kept = append(kept, ln)
		}
	}
	return pystr.Strip(strings.Join(kept, "\n"))
}

// countImagesAbove adds to the buffer the figures on the page whose top is
// at or above y, from next on, and returns the index of the first not
// counted.
func (b *blockBuffer) countImagesAbove(tops []float64, next int, y float64) int {
	for next < len(tops) && tops[next] <= y {
		b.images++
		next++
	}
	return next
}

// blockSink turns full buffers into blocks under the current section.
type blockSink struct {
	// path renders the heading path of a section.
	path    func(section string) []string
	blocks  []domain.Block
	section string
	buf     blockBuffer
	// printedOffset is subtracted from a block's page to give the page
	// number printed on it.
	printedOffset int
	inSection     bool
}

// flush emits the buffer as a block, when it holds text, and empties it.
func (s *blockSink) flush() {
	if text := s.buf.text(); text != "" && s.buf.page != 0 {
		block := domain.Block{
			Locator:     map[string]int{"page": s.buf.page, "page_end": s.buf.pageEnd},
			PrintedPage: ptr(s.buf.page - s.printedOffset),
			Text:        text,
			HeadingPath: []string{},
			ImageCount:  s.buf.images,
			Subdivision: s.buf.subdivision,
		}
		if s.inSection {
			block.Section = ptr(s.section)
			block.HeadingPath = s.path(s.section)
		}
		s.blocks = append(s.blocks, block)
	}
	s.buf = blockBuffer{}
}

// enter flushes the current block and starts section.
func (s *blockSink) enter(section string) {
	s.flush()
	s.section, s.inSection = section, true
}

func ptr[T any](v T) *T { return &v }

// outlinePath renders a section's heading path from outline titles alone:
// its numbers are synthesised from the outline's nesting and appear nowhere
// in the document. Ancestors without a title are left out.
func outlinePath(titles map[string]string) func(string) []string {
	return func(section string) []string {
		out := []string{}
		for _, a := range ancestors(section) {
			if t := pystr.Strip(titles[a]); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
}

// numberedPath renders a section's heading path with the numbers the
// document prints: "5. Setup", "5.2. Network".
func numberedPath(titles map[string]string) func(string) []string {
	return func(section string) []string {
		out := []string{}
		for _, a := range ancestors(section) {
			out = append(out, pystr.Strip(a+". "+titles[a]))
		}
		return out
	}
}

// emitOutlineBlocks emits blocks whose boundaries come from outline
// placements: a heading applies from its position on the page onward.
// Pages in skip, the printed contents, contribute no text, but the
// headings placed on them still apply, or the section a listing sits under
// would annex its later pages to whatever came before.
func emitOutlineBlocks(pages []Page, placements []placement, titles map[string]string,
	boiler boilerplate, figureTops [][]float64, skip map[int]bool,
) []domain.Block {
	byPage := map[int][]placement{}
	for _, p := range placements {
		byPage[p.page] = append(byPage[p.page], p)
	}
	sink := &blockSink{path: outlinePath(titles)}
	for pno, p := range pages {
		pending := slices.Clone(byPage[pno])
		slices.SortStableFunc(pending, func(a, b placement) int { return cmpFloat(a.y, b.y) })
		if skip[pno] {
			sink.enterAll(pending)
			continue
		}
		pending, counted := emitOutlinePage(sink, p.Lines, pno, pending, boiler, figureTops[pno])
		sink.enterAll(pending)
		// Figures below the last line belong to whatever block is open.
		sink.buf.images += max(0, len(figureTops[pno])-counted)
	}
	sink.flush()
	return sink.blocks
}

// enterAll enters each placed heading in turn.
func (s *blockSink) enterAll(headings []placement) {
	for _, h := range headings {
		s.enter(h.section)
	}
}

// emitOutlinePage adds one page's lines, entering each placed heading at or
// above a line before reading it. A heading placed at the top of the page
// (y -1) applies before the first line, as the outline claimed. A line a
// heading was matched against is the heading itself and is not text. It
// returns the headings still pending below the last line and how many of the
// page's figures were counted.
func emitOutlinePage(sink *blockSink, lines []Line, pno int, pending []placement,
	boiler boilerplate, tops []float64,
) ([]placement, int) {
	next := 0
	for _, ln := range lines {
		consumed := false
		for len(pending) > 0 && pending[0].y <= ln.Y0 {
			sink.enter(pending[0].section)
			consumed = consumed || pending[0].matched
			pending = pending[1:]
		}
		if consumed || boiler.matches(ln.Text) {
			continue
		}
		next = sink.buf.countImagesAbove(tops, next, ln.Y0)
		sink.buf.add(ln.Text, pno)
	}
	return pending, next
}

// numberedLayout is what emitNumberedBlocks reads: the body headings placed
// on their pages, and where the body starts and stops.
type numberedLayout struct {
	boiler       boilerplate
	subdivisions map[pagePoint]bool
	// headings maps a page to the sections whose heading sits on it, keyed
	// by the heading line's top rounded to a tenth.
	headings     map[int]map[float64]string
	titles       map[string]string
	indexSection string
	pages        []Page
	figureTops   [][]float64
	// tocPageMax is the last printed contents page, -1 when there is none.
	tocPageMax    int
	printedOffset int
	hasIndex      bool
}

// emitNumberedBlocks emits blocks whose boundaries are body headings found
// by font and number, stopping where the back-of-book index begins: the
// index is read into index terms, not text.
func emitNumberedBlocks(l numberedLayout) []domain.Block {
	sink := &blockSink{path: numberedPath(l.titles), printedOffset: l.printedOffset}
	for pno, p := range l.pages {
		if pno <= l.tocPageMax {
			continue
		}
		if l.inIndex(sink) {
			break
		}
		counted := l.emitPage(sink, p.Lines, pno)
		sink.buf.images += max(0, len(l.figureTops[pno])-counted)
	}
	sink.flush()
	return sink.blocks
}

// emitPage adds one page's lines, entering a section at its heading line
// and starting a new block at each subdivision point. It returns how many of
// the page's figures were counted.
func (l numberedLayout) emitPage(sink *blockSink, lines []Line, pno int) int {
	tops, next := l.figureTops[pno], 0
	for _, ln := range lines {
		if l.boiler.matches(ln.Text) {
			continue
		}
		next = sink.buf.countImagesAbove(tops, next, ln.Y0)
		y := pystr.Round(ln.Y0, 1)
		section, isHeading := l.headings[pno][y]
		if !isHeading {
			l.addLine(sink, ln.Text, pagePoint{page: pno, y: y})
			continue
		}
		sink.enter(section)
		if l.inIndex(sink) {
			break
		}
	}
	return next
}

// addLine adds a body line, starting a new block first when the line is a
// subdivision point.
func (l numberedLayout) addLine(sink *blockSink, text string, at pagePoint) {
	if l.subdivisions[at] {
		sink.flush()
		sink.buf.subdivision = true
	}
	sink.buf.add(text, at.page)
}

func (l numberedLayout) inIndex(sink *blockSink) bool {
	return l.hasIndex && sink.inSection && sink.section == l.indexSection
}
