package pdf

import (
	"slices"
	"strconv"
	"strings"

	"github.com/bamsammich/docsearch/internal/pystr"
)

const (
	// contentsPageMinLines is the fewest lines a page needs before the share
	// of them naming sections means anything.
	contentsPageMinLines = 5
	// contentsPageMatchFraction is the share of a page's lines that must
	// name a known section for the page to be a printed contents listing.
	contentsPageMatchFraction = 0.5
)

// tocEntry is one section the document declares: in its outline, or in its
// printed contents.
type tocEntry struct {
	section string
	title   string
	page    int
}

// outlineEntries numbers the outline's entries by their nesting, "2.3" for
// the third child of the second top-level entry. The numbers appear nowhere
// in the document; they key the nesting.
func outlineEntries(outline []OutlineEntry) []tocEntry {
	entries := make([]tocEntry, 0, len(outline))
	var counters []int
	for _, e := range outline {
		if len(counters) > e.Level {
			counters = counters[:e.Level]
		}
		for len(counters) < e.Level {
			counters = append(counters, 0)
		}
		counters[e.Level-1]++
		parts := make([]string, len(counters))
		for i, c := range counters {
			parts[i] = strconv.Itoa(c)
		}
		entries = append(entries, tocEntry{
			section: strings.Join(parts, "."),
			title:   pystr.Strip(e.Title),
			page:    e.Page,
		})
	}
	return entries
}

// reconstructFrontTOC recovers section, title and printed page from printed
// contents pages among the first scanPages. A printed contents page is laid
// out in columns, so reading order interleaves numbers and titles; grouping
// lines into rows and reading each row left to right restores them.
//
// It also returns the pages that yielded entries, so body extraction skips
// exactly those. Inferring the contents' extent from "a page with a bare
// numbered line" would swallow real chapters, whose pages carry numbered
// headings too.
func reconstructFrontTOC(
	pages []Page,
	boiler boilerplate,
	scanPages int,
) ([]tocEntry, map[int]bool) {
	var entries []tocEntry
	tocPages := map[int]bool{}
	for pno, p := range pages[:min(scanPages, len(pages))] {
		keys, rows := bands(p.Lines, boiler)
		for _, key := range keys {
			if e, ok := contentsRow(texts(rows[key])); ok {
				entries = append(entries, e)
				tocPages[pno] = true
			}
		}
	}
	return entries, tocPages
}

// contentsRow reads one row of a printed contents page: a section number,
// title cells, and a page number. A first cell holding "7.17. Title" as one
// line is split, since a long section number can leave the line builder too
// little gap to keep it apart from its title.
func contentsRow(cells []string) (tocEntry, bool) {
	if len(cells) >= 2 && !sectionOnly.MatchString(cells[0]) {
		if m := sectionMerged.FindStringSubmatch(cells[0]); m != nil {
			cells = append([]string{m[1] + ".", m[2]}, cells[1:]...)
		}
	}
	if len(cells) < 3 {
		return tocEntry{}, false
	}
	head := sectionOnly.FindStringSubmatch(cells[0])
	page, err := pystr.Atoi(cells[len(cells)-1])
	if head == nil || err != nil {
		return tocEntry{}, false
	}
	title := pystr.Strip(strings.Join(cells[1:len(cells)-1], " "))
	if title == "" {
		return tocEntry{}, false
	}
	return tocEntry{section: head[1], title: title, page: page}, true
}

// contentsPages finds pages that list the document's own sections.
//
// An embedded outline names the printed contents as a section like any
// other, so its pages would become chunks: text duplicating the heading
// structure, matching broadly and answering nothing. A page most of whose
// lines name known sections is a listing whatever the document calls it,
// where matching the words "Table of Contents" would not survive another
// language.
func contentsPages(pages []Page, entries []tocEntry, boiler boilerplate) map[int]bool {
	titles := map[string]bool{}
	for _, e := range entries {
		if pystr.Strip(e.title) != "" {
			titles[normalizeTitle(e.title)] = true
		}
	}
	found := map[int]bool{}
	if len(titles) == 0 {
		return found
	}
	for pno, p := range pages {
		if isListing(p.Lines, titles, boiler) {
			found[pno] = true
		}
	}
	return found
}

// isListing reports whether most of a page's lines name a known section.
func isListing(lines []Line, titles map[string]bool, boiler boilerplate) bool {
	body, matched := 0, 0
	for _, ln := range lines {
		if pystr.Strip(ln.Text) == "" || boiler.matches(ln.Text) {
			continue
		}
		body++
		if titles[normalizeTitle(contentsLeader.ReplaceAllString(ln.Text, ""))] {
			matched++
		}
	}
	return body >= contentsPageMinLines &&
		float64(matched)/float64(body) >= contentsPageMatchFraction
}

// placement is an outline entry placed at a position on its page.
type placement struct {
	section string
	title   string
	page    int
	y       float64
	// matched is true when the title was found among the page's lines; an
	// unmatched entry sits at the top of its page, y -1.
	matched bool
}

// locateOutlineHeadings places each outline entry on the page it names.
//
// The outline is authoritative about which sections exist, how they nest,
// and the page each begins on. It does not carry the position within the
// page, so the title is matched against the page's lines to recover it. An
// entry whose title is not on its page goes to the top of the page: the
// outline still declared the page, and page granularity is the honest
// resolution rather than grounds to refuse the document.
func locateOutlineHeadings(
	pages []Page,
	entries []tocEntry,
	boiler boilerplate,
) ([]placement, int) {
	var out []placement
	located := 0
	for _, e := range entries {
		pno := e.page - 1
		if pno < 0 || pno >= len(pages) {
			continue
		}
		y, ok := findTitle(pages[pno].Lines, normalizeTitle(e.title), boiler)
		if ok {
			located++
		}
		out = append(
			out,
			placement{section: e.section, title: e.title, page: pno, y: y, matched: ok},
		)
	}
	slices.SortStableFunc(out, func(a, b placement) int {
		if a.page != b.page {
			return a.page - b.page
		}
		return cmpFloat(a.y, b.y)
	})
	return out, located
}

// findTitle is the top of the first line whose normalized text is want or
// begins with it, or -1 and false.
func findTitle(lines []Line, want string, boiler boilerplate) (float64, bool) {
	if want == "" {
		return -1, false
	}
	for _, ln := range lines {
		if boiler.matches(ln.Text) {
			continue
		}
		if got := normalizeTitle(ln.Text); got != "" && strings.HasPrefix(got, want) {
			return ln.Y0, true
		}
	}
	return -1, false
}
