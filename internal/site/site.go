// Package site turns a crawl into one extraction: a site is a book whose
// chapters are its pages.
//
// Its navigation declares which sections exist and how they nest, exactly as
// a PDF's embedded outline does, so each page becomes an authoritative
// section keyed by its position in that tree, and the page's own h1 to h6
// subdivide beneath it. Nothing here is a new chunking strategy: it is the
// existing one, handed a section it already knows what to do with.
package site

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bamsammich/docsearch/internal/adapter/html"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
	"github.com/bamsammich/docsearch/internal/site/crawl"
	"github.com/bamsammich/docsearch/internal/site/nav"
)

const (
	// chromePageFraction is the share of a site's pages a block must appear
	// on to be furniture rather than content. A rendered navigation hits
	// ~100%; the same sentence on half a manual's pages is furniture whatever
	// it says.
	//
	// The HTML parse already drops nav and footer, which catches sites that
	// use them. Plenty do not: a sidebar is routinely a div full of links,
	// and those survive as a block on every single page.
	chromePageFraction = 0.5
	// chromeMinPages is the page count below which repetition is not
	// evidence: three pages of a five-page site sharing a sentence is
	// ordinary.
	chromeMinPages = 5
	// chromeMaxChars is the longest block that can be chrome. Chrome is a
	// link label, a breadcrumb, a cookie notice; a long passage repeated
	// across a site is duplicated content, a different defect and not one to
	// fix by deletion.
	chromeMaxChars = 200
)

// BuildExtraction turns a crawl into the extraction the chunker consumes.
// title overrides the name the site is given, where a caller supplied one.
func BuildExtraction(result *crawl.Result, title string) (*domain.Extraction, error) {
	hierarchy := result.Hierarchy
	placements := hierarchy.ByURL()
	pages, unplaced := orderedPages(result, placements)

	// Parsed up front, because whether a block is chrome is a fact about the
	// whole site and cannot be decided while looking at one page.
	parsed := make([]parsedPage, 0, len(pages))
	for _, page := range pages {
		p, err := parsePage(page, placements[page.URL])
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, p)
	}

	chrome := chromeTexts(parsed)
	blocks, dropped := blocksOf(parsed, placements, chrome)
	siteTitle, err := Title(result, title)
	if err != nil {
		return nil, err
	}
	ext := domain.NewExtraction(siteTitle, "site", hierarchy.Source, blocks)
	ext.Diagnostics["site"] = diagnostics(siteReport{
		result: result, hierarchy: hierarchy, blocks: blocks,
		unplaced: unplaced, chromeDropped: dropped, chromeDistinct: len(chrome),
	})
	ext.Diagnostics["notes"] = result.Notes
	return ext, nil
}

// parsedPage is one page, read.
type parsedPage struct {
	url   string
	title string
	items []html.Item
}

// orderedPages lists the fetched pages in navigation order, and the pages no
// placement covers.
//
// Navigation order rather than fetch order: chunk ordinals are document
// order, and for a site the document's order is what its navigation says, not
// the sequence a sitemap happened to list or a walk happened to reach.
func orderedPages(
	result *crawl.Result,
	placements map[string]nav.Placement,
) ([]*crawl.Page, []string) {
	pages := make([]*crawl.Page, 0, len(result.Pages))
	var unplaced []string
	for _, u := range result.Order {
		if _, ok := placements[u]; !ok {
			// nav places every fetched page, by URL path where no source
			// named it, so this is a defect rather than an ordinary outcome.
			unplaced = append(unplaced, u)
			continue
		}
		pages = append(pages, result.Pages[u])
	}
	slices.SortStableFunc(pages, func(a, b *crawl.Page) int {
		return slices.Compare(
			sectionKey(placements[a.URL].Section),
			sectionKey(placements[b.URL].Section),
		)
	})
	return pages, unplaced
}

// parsePage reads one page's blocks and settles what to call it.
func parsePage(page *crawl.Page, placement nav.Placement) (parsedPage, error) {
	htmlTitle, items, err := html.Parse(string(page.Body))
	if err != nil {
		return parsedPage{}, fmt.Errorf("%s: %w", page.URL, err)
	}
	title := firstNonEmpty(placement.Title, page.DeclaredTitle, htmlTitle, page.URL)
	return parsedPage{url: page.URL, title: title, items: items}, nil
}

// blocksOf turns every page's items into blocks, dropping the site's chrome,
// and returns how many blocks were dropped.
func blocksOf(
	parsed []parsedPage,
	placements map[string]nav.Placement,
	chrome map[string]bool,
) ([]domain.Block, int) {
	blocks := []domain.Block{}
	offset, dropped := 0, 0
	for _, page := range parsed {
		pageBlocks, pageDropped := pageBlocksOf(page, placements[page.url], chrome, offset)
		for _, b := range pageBlocks {
			offset += utf8.RuneCountInString(b.Text) + 1
		}
		blocks = append(blocks, pageBlocks...)
		dropped += pageDropped
	}
	return blocks, dropped
}

// pageBlocksOf turns one page's items into blocks, numbering them from
// offset, and returns how many the site's chrome swallowed.
func pageBlocksOf(page parsedPage, placement nav.Placement, chrome map[string]bool,
	offset int,
) ([]domain.Block, int) {
	base := append(append([]string{}, placement.Ancestry...), page.title)
	var blocks []domain.Block
	dropped := 0
	for _, item := range page.items {
		block, ok := blockOf(item, base, page, placement, chrome, offset)
		if !ok {
			if chrome[normalizeText(pystr.Strip(item.Text))] {
				dropped++
			}
			continue
		}
		blocks = append(blocks, block)
		offset += utf8.RuneCountInString(block.Text) + 1
	}
	return blocks, dropped
}

// blockOf turns one item into a block, unless it is empty or the site's
// chrome.
func blockOf(item html.Item, base []string, page parsedPage, placement nav.Placement,
	chrome map[string]bool, offset int,
) (domain.Block, bool) {
	text := pystr.Strip(item.Text)
	if text == "" || chrome[normalizeText(text)] {
		return domain.Block{}, false
	}
	block := domain.NewOffsetBlock(pagePath(base, item.HeadingPath, page.title), offset, text)
	block.Section = &placement.Section
	block.URL = &page.url
	block.Fragment = item.Fragment
	return block, true
}

// pagePath is one item's heading ancestry: its place in the navigation, then
// its place on the page.
//
// A page whose first heading repeats its navigation title would otherwise
// carry that title twice, which reads as a level of structure the document
// does not have and splits the heading path a section filter matches on.
func pagePath(base, itemPath []string, pageTitle string) []string {
	inner := make([]string, 0, len(itemPath))
	for _, p := range itemPath {
		if p != "" {
			inner = append(inner, p)
		}
	}
	if len(inner) > 0 && normalizeText(inner[0]) == normalizeText(pageTitle) {
		inner = inner[1:]
	}
	return append(append([]string{}, base...), inner...)
}

// chromeTexts finds the blocks repeated across enough of the site to be
// furniture.
//
// The same reasoning as the PDF adapter's running headers, and the same
// trade: found by how often a block repeats rather than by where it sits,
// because where a generator puts its navigation in the markup says nothing
// about whether it is content.
//
// Counted per page rather than per occurrence, so a sidebar listing one label
// twice on a page does not count twice toward being furniture.
func chromeTexts(parsed []parsedPage) map[string]bool {
	chrome := map[string]bool{}
	if len(parsed) < chromeMinPages {
		return chrome
	}
	threshold := max(chromeMinPages, int(float64(len(parsed))*chromePageFraction))
	for key, pages := range pagesPerBlock(parsed) {
		if len(pages) >= threshold {
			chrome[key] = true
		}
	}
	return chrome
}

// pagesPerBlock maps each short block to the pages it appears on. Counted
// per page rather than per occurrence, so a sidebar listing one label twice
// on a page does not count twice toward being furniture.
func pagesPerBlock(parsed []parsedPage) map[string]map[string]bool {
	onPages := map[string]map[string]bool{}
	for _, page := range parsed {
		for _, item := range page.items {
			addCandidate(onPages, item.Text, page.url)
		}
	}
	return onPages
}

// addCandidate records that a page holds a block short enough to be chrome.
func addCandidate(onPages map[string]map[string]bool, text, pageURL string) {
	text = pystr.Strip(text)
	if text == "" || utf8.RuneCountInString(text) > chromeMaxChars {
		return
	}
	key := normalizeText(text)
	if onPages[key] == nil {
		onPages[key] = map[string]bool{}
	}
	onPages[key][pageURL] = true
}

// Title is a name for the whole site.
//
// The seed page's title is what the publisher calls the site; the host is the
// honest fallback, and beats naming the document after whichever page
// happened to be fetched first.
func Title(result *crawl.Result, override string) (string, error) {
	if override != "" {
		return pystr.Strip(override), nil
	}
	if seed, ok := result.Pages[result.Seed]; ok {
		title, _, err := html.Parse(string(seed.Body))
		if err != nil {
			return "", fmt.Errorf("%s: %w", result.Seed, err)
		}
		if t := pystr.Strip(title); t != "" {
			return t, nil
		}
	}
	u, err := url.Parse(result.Seed)
	if err != nil || u.Hostname() == "" {
		return result.Seed, nil //nolint:nilerr // an unparseable seed is its own name
	}
	return u.Hostname(), nil
}

// siteReport is what the "site" diagnostic is built from.
type siteReport struct {
	result         *crawl.Result
	hierarchy      *nav.Hierarchy
	blocks         []domain.Block
	unplaced       []string
	chromeDropped  int
	chromeDistinct int
}

// diagnostics is the "site" diagnostic the structure report grades.
func diagnostics(r siteReport) map[string]any {
	result, hierarchy := r.result, r.hierarchy
	withBlocks := map[string]bool{}
	for _, b := range r.blocks {
		if b.URL != nil {
			withBlocks[*b.URL] = true
		}
	}
	unreachable := make([]string, 0, len(result.Unreachable))
	reasons := make([]string, 0, len(result.Unreachable))
	for _, u := range result.Unreachable {
		unreachable = append(unreachable, u.URL)
		reasons = append(reasons, u.URL+": "+u.Reason)
	}
	return map[string]any{
		"seed":                   result.Seed,
		"pages_declared":         result.Declared(),
		"pages_fetched":          len(result.Pages),
		"pages_with_blocks":      len(withBlocks),
		"unreachable":            unreachable,
		"unreachable_reasons":    reasons,
		"placed_by_path":         orEmpty(hierarchy.PlacedByPath),
		"hierarchy_inferred":     hierarchy.Inferred,
		"canonical_merges":       len(result.CanonicalMerges),
		"unplaced_pages":         orEmpty(r.unplaced),
		"chrome_blocks_dropped":  r.chromeDropped,
		"chrome_distinct_blocks": r.chromeDistinct,
	}
}

// sectionKey is a dotted section as integers, for ordering.
func sectionKey(section string) []int {
	parts := strings.Split(section, ".")
	key := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := pystr.Atoi(p)
		if err != nil {
			return nil
		}
		key = append(key, n)
	}
	return key
}

// normalizeText collapses whitespace and lowercases, so two spellings of one
// label compare equal.
func normalizeText(text string) string {
	return pystr.Lower(strings.Join(strings.FieldsFunc(text, pystr.IsSpace), " "))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func orEmpty(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
