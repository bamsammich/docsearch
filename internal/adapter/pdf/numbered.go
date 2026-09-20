package pdf

import (
	"maps"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
)

// numbered builds blocks from body headings found by font and number, for a
// document with printed contents or with font sizes alone.
type numbered struct {
	pages        []Page
	entries      []tocEntry
	titles       map[string]string
	headingSizes []float64
	boiler       boilerplate
	figureTops   [][]float64
	tocPageMax   int
}

func (n numbered) blocks(diagnostics map[string]any) ([]domain.Block, [][2]string) {
	scan := findBodyHeadings(n.pages, n.headingSizes, n.boiler, n.tocPageMax)
	diagnostics["body_headings_found"] = len(scan.headings)
	diagnostics["candidates_rejected_by_ordering"] = scan.rejected
	diagnostics["cross_validation"] = crossValidation(n.titles, scan.headings)

	sectionTitles := maps.Clone(n.titles)
	for _, h := range scan.headings {
		if _, ok := sectionTitles[h.section]; !ok {
			sectionTitles[h.section] = h.title
		}
	}
	offset := printedPageOffset(n.entries, scan.headings)
	diagnostics["printed_page_offset"] = offset

	indexSection, hasIndex := n.indexSection()
	indexTerms := [][2]string{}
	if hasIndex {
		indexTerms = n.indexTerms(indexSection, scan.headings, sectionTitles, diagnostics)
	}
	blocks := emitNumberedBlocks(numberedLayout{
		pages:         n.pages,
		boiler:        n.boiler,
		subdivisions:  scan.subdivisions,
		headings:      placeBodyHeadings(n.pages, scan.headings),
		titles:        sectionTitles,
		figureTops:    n.figureTops,
		tocPageMax:    n.tocPageMax,
		printedOffset: offset,
		indexSection:  indexSection,
		hasIndex:      hasIndex,
	})
	return blocks, indexTerms
}

// indexSection is the first declared section titled "Index".
func (n numbered) indexSection() (string, bool) {
	for _, e := range n.entries {
		if pystr.Lower(pystr.Strip(e.title)) == "index" {
			return e.section, true
		}
	}
	return "", false
}

// indexTerms parses the index from the page its heading was found on, and
// keeps the terms that point at a known section.
func (n numbered) indexTerms(section string, headings []bodyHeading,
	titles map[string]string, diagnostics map[string]any,
) [][2]string {
	start := -1
	for _, h := range headings {
		if h.section == section {
			start = h.page
			break
		}
	}
	if start < 0 {
		return [][2]string{}
	}
	parsed := parseIndex(n.pages, n.boiler, start)
	known := [][2]string{}
	for _, t := range parsed {
		if _, ok := titles[t[1]]; ok {
			known = append(known, t)
		}
	}
	diagnostics["index"] = map[string]any{
		"start_page":                       start + 1,
		"entries":                          len(parsed),
		"refs_resolving_to_known_sections": len(known),
	}
	return known
}

// crossValidation compares the declared sections with those found in the
// body. Set differences cannot see a section found twice, whose number is
// legitimately declared, so detections are counted too.
func crossValidation(titles map[string]string, headings []bodyHeading) map[string]any {
	detected := map[string]int{}
	for _, h := range headings {
		detected[h.section]++
	}
	missing := sectionsWhere(
		titles,
		func(section string, _ string) bool { return detected[section] == 0 },
	)
	extra := sectionsWhere(detected, func(section string, _ int) bool {
		_, declared := titles[section]
		return len(titles) > 0 && !declared
	})
	duplicates := sectionsWhere(detected, func(_ string, count int) bool { return count > 1 })
	return map[string]any{
		"toc_sections":            len(titles),
		"body_sections":           len(detected),
		"in_toc_not_in_body":      missing,
		"in_body_not_in_toc":      extra,
		"detected_more_than_once": duplicates,
	}
}

// sectionsWhere lists the sections of m that keep holds for, in section
// order, empty rather than nil.
func sectionsWhere[V any](m map[string]V, keep func(string, V) bool) []string {
	out := []string{}
	for section, v := range m {
		if keep(section, v) {
			out = append(out, section)
		}
	}
	sortSections(out)
	return out
}

// printedPageOffset is how far the physical page runs ahead of the printed
// page number, measured on the headings whose declared page is known: the
// most common difference, the first seen on a tie.
func printedPageOffset(entries []tocEntry, headings []bodyHeading) int {
	declared := map[string]int{}
	for _, e := range entries {
		declared[e.section] = e.page
	}
	var offsets []int
	for _, h := range headings {
		if page, ok := declared[h.section]; ok {
			offsets = append(offsets, h.page+1-page)
		}
	}
	return mostCommon(offsets)
}

// mostCommon is the value seen most often, the first seen on a tie, as
// Python's Counter.most_common(1) picks it; 0 for no values.
func mostCommon(values []int) int {
	counts := map[int]int{}
	best := 0
	for i, v := range values {
		counts[v]++
		if i == 0 {
			best = v
		}
	}
	for _, v := range values {
		if counts[v] > counts[best] {
			best = v
		}
	}
	return best
}

// placeBodyHeadings finds each heading's line on its page, keyed by the
// line's top rounded to a tenth.
func placeBodyHeadings(pages []Page, headings []bodyHeading) map[int]map[float64]string {
	out := map[int]map[float64]string{}
	for _, h := range headings {
		if out[h.page] == nil {
			out[h.page] = map[float64]string{}
		}
		out[h.page][pystr.Round(headingTop(pages[h.page].Lines, h.section), 1)] = h.section
	}
	return out
}

// headingTop is the top of the first line numbered section, 0 when none is,
// which places the heading at the top of its page.
func headingTop(lines []Line, section string) float64 {
	for _, ln := range lines {
		if m := sectionLine.FindStringSubmatch(ln.Text); m != nil && m[1] == section {
			return ln.Y0
		}
	}
	return 0
}
