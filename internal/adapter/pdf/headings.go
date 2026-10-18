package pdf

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// bodyHeading is a numbered section heading found in the body.
type bodyHeading struct {
	section string
	title   string
	page    int
}

// pagePoint is a position on a page: the page index and a line's top,
// rounded to a tenth of a point.
type pagePoint struct {
	page int
	y    float64
}

// headingScan is what findBodyHeadings returns.
type headingScan struct {
	// subdivisions are heading-sized lines with no section number, where an
	// oversized section may split.
	subdivisions map[pagePoint]bool
	headings     []bodyHeading
	// rejected lists candidates refused by the ordering rule, "p12:1".
	rejected []string
	// lists are the pages, 1-based, whose numbered lines were read as a
	// numbered list rather than as section headings.
	lists []int
}

// findBodyHeadings locates numbered section headings in the body, after the
// printed contents pages.
//
// A numbered procedure step ("1. Tap the title bar") at a heading size is
// indistinguishable from a chapter heading by font and pattern alone.
// Document order tells them apart: section numbering only ever advances, so
// a candidate that does not sort after the last accepted heading is a list
// item. Without the rule a stray "1." resets the section mid-book and
// scatters one section across the document.
func findBodyHeadings(
	pages []Page,
	headingSizes []float64,
	boiler boilerplate,
	tocPageMax int,
) headingScan {
	s := headingScanner{
		headingScan: headingScan{
			subdivisions: map[pagePoint]bool{}, rejected: []string{}, lists: []int{},
		},
		sizes:  headingSizes,
		boiler: boiler,
	}
	for pno, p := range pages {
		if pno <= tocPageMax {
			continue
		}
		s.onList = s.numberedList(p.Lines)
		if s.onList {
			s.lists = append(s.lists, pno+1)
		}
		for i := 0; i < len(p.Lines); i++ {
			i = s.line(pno, p.Lines, i)
		}
	}
	return s.headingScan
}

// A numbered list set at a heading size reads, line by line, exactly like
// section numbering: safety instructions "1." to "18." at a size above the
// body become eighteen sections, the last of which annexes the rest of the
// book. What gives a list away is its items, which are sentences where a
// section's are titles. A page with at least minListItems numbered lines,
// most of which read as sentences, is a list.
const (
	minListItems = 3
	// maxTitleWords is the longest a numbered heading's title runs before
	// it reads as running text.
	maxTitleWords = 10
)

// sentenceEnd is the end of a sentence: a full stop, question or
// exclamation mark, closing any quote or bracket, then the next sentence or
// the end of the line.
var sentenceEnd = regexp.MustCompile(`[.!?]["”’)]?(\s+\p{Lu}|\s*$)`)

// readsAsSentence reports whether a numbered line's text after the number
// is running text rather than a title.
func readsAsSentence(title string) bool {
	return sentenceEnd.MatchString(pystr.Strip(title)) ||
		len(strings.Fields(title)) > maxTitleWords
}

// numberedList reports whether a page's numbered heading-size lines are a
// numbered list.
func (s *headingScanner) numberedList(lines []Line) bool {
	numbered, sentences := 0, 0
	for _, ln := range lines {
		if !slices.Contains(s.sizes, ln.Size) || s.boiler.matches(ln.Text) {
			continue
		}
		m := sectionLine.FindStringSubmatch(ln.Text)
		if m == nil {
			continue
		}
		numbered++
		if readsAsSentence(m[2]) {
			sentences++
		}
	}
	return sentences >= minListItems && 2*sentences > numbered
}

// headingScanner reads a document's lines in order, remembering the last
// accepted section number.
type headingScanner struct {
	headingScan
	boiler boilerplate
	sizes  []float64
	last   []int
	// onList is whether the page being read is a numbered list, whose
	// numbered lines are body text.
	onList bool
}

// line considers lines[i] as a heading and returns the index of the last
// line it read, which is past i when the heading's title is the next line.
func (s *headingScanner) line(pno int, lines []Line, i int) int {
	ln := lines[i]
	if !slices.Contains(s.sizes, ln.Size) || s.boiler.matches(ln.Text) {
		return i
	}
	m := sectionLine.FindStringSubmatch(ln.Text)
	if m == nil {
		s.subdivisions[pagePoint{page: pno, y: pystr.Round(ln.Y0, 1)}] = true
		return i
	}
	if s.onList {
		return i
	}
	key := sectionKey(m[1])
	if slices.Compare(key, s.last) <= 0 {
		s.rejected = append(s.rejected, "p"+strconv.Itoa(pno+1)+":"+m[1])
		return i
	}
	title := pystr.Strip(m[2])
	if title == "" {
		title, i = splitTitle(lines, i)
	}
	s.last = key
	s.headings = append(s.headings, bodyHeading{section: m[1], title: title, page: pno})
	return i
}

// splitTitle reads the title of a chapter heading set as two lines, the
// number and then the title at the same size. It returns the title and the
// index of the line it came from, or "" and i when the next line is not one.
func splitTitle(lines []Line, i int) (string, int) {
	j := i + 1
	for j < len(lines) && pystr.Strip(lines[j].Text) == "" {
		j++
	}
	if j < len(lines) && lines[j].Size == lines[i].Size {
		return pystr.Strip(lines[j].Text), j
	}
	return "", i
}

// parseIndex reads a back-of-book index from startPage on into term and
// section pairs. Entries in this format reference section numbers rather
// than pages, and one term may carry several.
func parseIndex(pages []Page, boiler boilerplate, startPage int) [][2]string {
	var out [][2]string
	for _, p := range pages[startPage:] {
		keys, rows := bands(p.Lines, boiler)
		for _, key := range keys {
			out = append(out, indexRow(strings.Join(texts(rows[key]), " "))...)
		}
	}
	return out
}

// indexRow reads one index row, "term  1.2.  3.4.", into a pair per
// reference; nothing when the row is not an entry.
func indexRow(row string) [][2]string {
	m := indexEntry.FindStringSubmatch(pystr.Strip(row))
	if m == nil {
		return nil
	}
	term := pystr.Strip(m[1])
	if term == "" {
		return nil
	}
	refs := sectionRef.FindAllString(m[2], -1)
	out := make([][2]string, len(refs))
	for i, ref := range refs {
		out[i] = [2]string{term, ref}
	}
	return out
}
