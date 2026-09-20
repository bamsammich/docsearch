// Package nav says how a documentation site's pages nest.
//
// Hierarchy only. Which pages exist is a separate question with separate
// sources, answered in internal/site/discover. Keeping them apart is what
// the probed sites forced: a rendered sidebar is not a page set. One site has
// none at all, and another renders 19 links against 210 pages because its
// generator collapses categories in the browser.
//
// So a hierarchy source is checked against coverage before it is believed. A
// sidebar that places a tenth of the site is not the site's structure
// whatever it looks like, and pages no source mentions are placed by their
// URL path and reported rather than dropped: dropping them discarded 41 of
// one site's 210 pages, including every command reference.
//
// The output is what the chunker needs: for each page, a dotted section
// number and the heading ancestry above it.
//
// Ported from python/docsearch/nav.py.
package nav

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/bamsammich/docsearch/internal/adapter/html"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/site/fetch"
)

// minCoverage is the share of the page set a sidebar must place before it is
// treated as the site's structure. Below it, the sidebar is a fragment of a
// navigation rather than a map of one, which is what a generator that
// collapses its categories renders.
const minCoverage = 0.5

// navSelector matches the elements that plausibly hold a documentation
// navigation.
const navSelector = "nav, aside, [class*=sidebar], [class*=Sidebar], " +
	"[class*=toc], [class*=menu], [id*=sidebar]"

const headingSelector = "h1, h2, h3, h4, h5, h6"

// Placement is where one page sits in the tree.
type Placement struct {
	URL string
	// Section is the dotted position, "3.2", which becomes the chunk's
	// authoritative section.
	Section string
	Title   string
	// Ancestry is the heading ancestry above the page, root first.
	Ancestry []string
}

// Hierarchy is every page's placement, and how it was arrived at.
type Hierarchy struct {
	Placements []Placement
	// PlacedByPath are the pages no declared source mentioned.
	PlacedByPath []string
	Notes        []string
	// Source is domain.SourceSidebarDOM, domain.SourceIndexPage or
	// domain.SourceURLPath.
	Source domain.StructureSource
	// Inferred is true when the structure was read from URLs rather than
	// declared, so nothing exists to check it against.
	Inferred bool
}

// ByURL indexes the placements.
func (h *Hierarchy) ByURL() map[string]Placement {
	out := make(map[string]Placement, len(h.Placements))
	for _, p := range h.Placements {
		out[p.URL] = p
	}
	return out
}

// node is one entry of a navigation tree.
type node struct {
	title string
	// url is empty for a category that names a level without being a page.
	url      string
	children []*node
}

// Derive chooses a hierarchy source, places every page, and says what
// happened. seedHTML is the seed page, where the crawl read one.
func Derive(coverage []string, seed string, seedHTML []byte) (*Hierarchy, error) {
	h := &Hierarchy{Source: domain.SourceURLPath, Inferred: true}
	if len(coverage) == 0 {
		return h, nil
	}
	best, err := bestCandidate(h, seedHTML, seed, coverage)
	if err != nil {
		return nil, err
	}
	if best.share < minCoverage {
		// A fragment of a navigation is not a map of one. Believing it would
		// index a tenth of the site under a confident-looking tree.
		if best.share > 0 {
			h.Notes = append(h.Notes, fmt.Sprintf(
				"%s places only %s of the page set; falling back to URL path depth",
				best.name, percent(best.share)))
		}
		h.Placements = flatten(urlPathTree(coverage, seed), "")
		h.PlacedByPath = coverage
		return h, nil
	}
	h.place(best, coverage, seed)
	return h, nil
}

// candidate is one hierarchy source and how much of the page set it places.
type candidate struct {
	placements []Placement
	share      float64
	name       domain.StructureSource
}

// bestCandidate reads the declared sources the seed page offers and returns
// whichever places most of the page set.
func bestCandidate(
	h *Hierarchy,
	seedHTML []byte,
	seed string,
	coverage []string,
) (candidate, error) {
	best := candidate{name: domain.SourceURLPath}
	if seedHTML == nil {
		return best, nil
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(seedHTML)))
	if err != nil {
		return best, fmt.Errorf("parse %s: %w", seed, err)
	}
	for _, source := range []struct {
		nodes []*node
		name  domain.StructureSource
	}{
		{nodes: sidebarTree(doc, seed), name: domain.SourceSidebarDOM},
		{nodes: indexPageTree(doc, seed), name: domain.SourceIndexPage},
	} {
		if len(source.nodes) == 0 {
			continue
		}
		placements := flatten(source.nodes, "")
		share := covered(placements, coverage)
		h.Notes = append(
			h.Notes,
			fmt.Sprintf("%s places %s of the page set", source.name, percent(share)),
		)
		if share > best.share {
			best = candidate{name: source.name, placements: placements, share: share}
		}
	}
	return best, nil
}

// place keeps the placements for pages the crawl knows, and places the rest
// by URL path. Excluding them would discard pages no navigation mentions,
// which is the silent partial index the completeness gate exists to prevent.
func (h *Hierarchy) place(best candidate, coverage []string, seed string) {
	wanted := make(map[string]bool, len(coverage))
	for _, u := range coverage {
		wanted[u] = true
	}
	var kept []Placement
	placed := map[string]bool{}
	for _, p := range best.placements {
		if wanted[p.URL] {
			kept = append(kept, p)
			placed[p.URL] = true
		}
	}
	var missing []string
	for _, u := range coverage {
		if !placed[u] {
			missing = append(missing, u)
		}
	}
	if len(missing) > 0 {
		kept = append(kept, flatten(urlPathTree(missing, seed), strconv.Itoa(len(kept)+1))...)
		h.PlacedByPath = missing
		h.Notes = append(h.Notes, fmt.Sprintf("%d page(s) absent from %s, placed by URL path",
			len(missing), best.name))
	}
	h.Source, h.Inferred, h.Placements = best.name, false, kept
}

// sidebarTree is the richest navigation the page renders, as a tree.
//
// The candidate container holding the most same-host links wins, rather than
// the first that matches, because a marketing header is a nav too. Whether
// the result is worth believing is decided by coverage, not here.
func sidebarTree(doc *goquery.Document, base string) []*node {
	doc = withoutScripts(doc)
	var best *goquery.Selection
	bestLinks := 0
	root(doc).Find(navSelector).Each(func(_ int, cand *goquery.Selection) {
		if n := sameHostLinks(cand, base); n > bestLinks {
			best, bestLinks = cand, n
		}
	})
	if best == nil {
		return nil
	}
	return treeFromLists(best, base, true)
}

// sameHostLinks counts the links in a container that address the same host.
func sameHostLinks(sel *goquery.Selection, base string) int {
	n := 0
	sel.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		if u, ok := absolute(a, base); ok && sameHost(u, base) {
			n++
		}
	})
	return n
}

// treeFromLists builds a tree from nested lists, or a flat list where the
// navigation has no list markup at all.
func treeFromLists(scope *goquery.Selection, base string, top bool) []*node {
	items := scope.ChildrenFiltered("ul, ol").ChildrenFiltered("li")
	if items.Length() == 0 {
		if !top {
			// A leaf item has no children. Falling through to the flat
			// branch would find the item's own link again and hang a copy of
			// the page beneath itself.
			return nil
		}
		return flatLinks(scope, base)
	}
	var out []*node
	items.Each(func(_ int, li *goquery.Selection) {
		n := itemNode(li, base)
		n.children = treeFromLists(li, base, false)
		out = append(out, n)
	})
	return out
}

// flatLinks is every link in a navigation with no list markup, in document
// order.
func flatLinks(scope *goquery.Selection, base string) []*node {
	var out []*node
	scope.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		if u, ok := absolute(a, base); ok {
			out = append(out, &node{title: text(a), url: u})
		}
	})
	return out
}

// itemNode is one list item: the page it links to, or the level it names.
func itemNode(li *goquery.Selection, base string) *node {
	if anchor := ownAnchor(li); anchor != nil {
		if u, ok := absolute(anchor, base); ok {
			return &node{title: text(anchor), url: u}
		}
	}
	// A category label with no link of its own still names a level.
	label := li.Find("span, button, div, " + headingSelector).First()
	title := text(li)
	if label.Length() > 0 {
		title = text(label)
	}
	return &node{title: strings.TrimSpace(strings.SplitN(title, "\n", 2)[0])}
}

// ownAnchor is the link belonging to a list item rather than to one of its
// children. Searching descendants alone would let a category item adopt its
// first child's link, taking that child's title and URL and then listing the
// child again beneath itself.
func ownAnchor(li *goquery.Selection) *goquery.Selection {
	var found *goquery.Selection
	li.Find("a[href]").EachWithBreak(func(_ int, a *goquery.Selection) bool {
		holder := a.ParentsFiltered("ul, ol").First()
		if holder.Length() > 0 && !sameNode(holder, li.Parent()) {
			return true
		}
		found = a
		return false
	})
	return found
}

// indexPageTree groups a hub page's links under the headings above them.
//
// A heading inside a link titles that link; a heading outside one opens a
// section. Both shapes appear on the same real page, where cards are written
// as a link wrapping a heading under a section header, and treating every
// heading as a section opener gives each link the title of the one before it.
func indexPageTree(doc *goquery.Document, base string) []*node {
	doc = withoutScripts(doc)
	doc.Find("nav, footer").Remove()
	b := &indexBuilder{base: base, seen: map[string]bool{}}
	root(doc).Find(headingSelector + ", a").Each(func(_ int, el *goquery.Selection) {
		b.visit(el)
	})
	return b.roots
}

// indexBuilder gathers a hub page's sections as its elements arrive.
type indexBuilder struct {
	base  string
	seen  map[string]bool
	roots []*node
	open  []openSection
}

// openSection is a heading still taking links, and the level it opened at.
type openSection struct {
	node  *node
	level int
}

func (b *indexBuilder) visit(el *goquery.Selection) {
	if goquery.NodeName(el) == "a" {
		b.addLink(el)
		return
	}
	if el.ParentsFiltered("a").Length() > 0 {
		// Titles a link; already read with it.
		return
	}
	b.openSection(el)
}

func (b *indexBuilder) addLink(a *goquery.Selection) {
	u, ok := absolute(a, b.base)
	if !ok || !sameHost(u, b.base) || b.seen[u] {
		return
	}
	b.seen[u] = true
	title := text(a)
	if inner := a.Find(headingSelector).First(); inner.Length() > 0 {
		title = text(inner)
	}
	b.add(&node{title: title, url: u})
}

func (b *indexBuilder) openSection(heading *goquery.Selection) {
	level, err := strconv.Atoi(goquery.NodeName(heading)[1:])
	if err != nil {
		return
	}
	for len(b.open) > 0 && b.open[len(b.open)-1].level >= level {
		b.open = b.open[:len(b.open)-1]
	}
	n := &node{title: text(heading)}
	b.add(n)
	b.open = append(b.open, openSection{node: n, level: level})
}

// add hangs a node under the open section, or at the root.
func (b *indexBuilder) add(n *node) {
	if len(b.open) == 0 {
		b.roots = append(b.roots, n)
		return
	}
	parent := b.open[len(b.open)-1].node
	parent.children = append(parent.children, n)
}

// urlPathTree nests pages by their path segments below the seed.
//
// Inferred, with nothing to check it against, which is why Derive warns. It
// still beats a flat list: it keeps a command reference together instead of
// scattering it.
func urlPathTree(urls []string, seed string) []*node {
	baseDepth := len(segmentsOf(seed))
	var roots []*node
	groups := map[string]*node{}
	for _, raw := range urls {
		segments := segmentsOf(raw)
		if len(segments) <= baseDepth {
			roots = append(roots, &node{title: titleFromSlug(pathOf(raw)), url: raw})
			continue
		}
		segments = segments[baseDepth:]
		children := &roots
		prefix := ""
		for _, seg := range segments[:len(segments)-1] {
			prefix += "/" + seg
			group, ok := groups[prefix]
			if !ok {
				group = &node{title: titleFromSlug(seg)}
				groups[prefix] = group
				*children = append(*children, group)
			}
			children = &group.children
		}
		*children = append(*children, &node{
			title: titleFromSlug(segments[len(segments)-1]), url: raw,
		})
	}
	return roots
}

// flatten numbers a tree depth first and keeps the first placement of each
// page: a page with two section numbers is two documents to anything that
// filters by section.
func flatten(nodes []*node, prefix string) []Placement {
	f := &flattener{}
	f.walk(nodes, prefix, nil)
	return f.placements
}

// flattener numbers a tree depth first, keeping the first placement of each
// page: a page with two section numbers is two documents to anything that
// filters by section.
type flattener struct {
	seen       map[string]bool
	placements []Placement
}

func (f *flattener) walk(items []*node, path string, ancestry []string) {
	for i, n := range items {
		section := strconv.Itoa(i + 1)
		if path != "" {
			section = path + "." + section
		}
		f.add(n, section, ancestry)
		next := ancestry
		if n.title != "" {
			next = append(append([]string{}, ancestry...), n.title)
		}
		f.walk(n.children, section, next)
	}
}

func (f *flattener) add(n *node, section string, ancestry []string) {
	if n.url == "" {
		return
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[n.url] {
		return
	}
	f.seen[n.url] = true
	f.placements = append(f.placements, Placement{
		URL: n.url, Section: section, Ancestry: ancestry, Title: titleOr(n, n.url),
	})
}

// covered is the share of the page set a source places.
func covered(placements []Placement, coverage []string) float64 {
	if len(coverage) == 0 {
		return 0
	}
	placed := make(map[string]bool, len(placements))
	for _, p := range placements {
		placed[p.URL] = true
	}
	n := 0
	for _, u := range coverage {
		if placed[u] {
			n++
		}
	}
	return float64(n) / float64(len(coverage))
}

// titleOr is a node's title, or one read from its URL.
func titleOr(n *node, raw string) string {
	if n.title != "" {
		return n.title
	}
	return titleFromSlug(pathOf(raw))
}

// titleFromSlug turns a path segment into a title.
func titleFromSlug(slug string) string {
	text := strings.Trim(slug, "/")
	if i := strings.LastIndex(text, "/"); i >= 0 {
		text = text[i+1:]
	}
	for _, suffix := range []string{".html", ".htm", ".md"} {
		text = strings.TrimSuffix(text, suffix)
	}
	text = strings.TrimSpace(strings.NewReplacer("-", " ", "_", " ").Replace(text))
	if text == "" {
		return "Untitled"
	}
	return titleCase(text)
}

// titleCase upper-cases each word's first letter, as Python's str.title()
// does for these slugs.
func titleCase(text string) string {
	words := strings.Split(text, " ")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}

// absolute is a link's target, resolved against the page it sits on, when it
// addresses a page at all.
func absolute(a *goquery.Selection, base string) (string, bool) {
	href, ok := a.Attr("href")
	if !ok || !isPageLink(strings.TrimSpace(href)) {
		return "", false
	}
	href = strings.TrimSpace(href)
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	next, err := baseURL.Parse(href)
	if err != nil {
		return "", false
	}
	u, err := fetch.Normalize(next.String())
	if err != nil {
		return "", false
	}
	return u, true
}

// isPageLink reports whether an href addresses a page rather than a
// fragment of this one or another scheme entirely.
func isPageLink(href string) bool {
	if href == "" || strings.HasPrefix(href, "#") {
		return false
	}
	for _, scheme := range []string{"mailto:", "javascript:", "tel:"} {
		if strings.HasPrefix(href, scheme) {
			return false
		}
	}
	return true
}

func sameHost(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil {
		return false
	}
	return strings.EqualFold(ua.Hostname(), ub.Hostname())
}

func segmentsOf(raw string) []string {
	var out []string
	for _, seg := range strings.Split(pathOf(raw), "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}

// text is a node's label, read as the adapters read prose.
func text(sel *goquery.Selection) string {
	if len(sel.Nodes) == 0 {
		return ""
	}
	return html.ProseText(sel.Nodes[0])
}

// withoutScripts drops the elements whose text is not content.
func withoutScripts(doc *goquery.Document) *goquery.Document {
	doc.Find("script, style").Remove()
	return doc
}

// root is the document's body, or the document itself where it has none.
func root(doc *goquery.Document) *goquery.Selection {
	if body := doc.Find("body").First(); body.Length() > 0 {
		return body
	}
	return doc.Selection
}

// sameNode reports whether two selections hold the same element.
func sameNode(a, b *goquery.Selection) bool {
	if len(a.Nodes) == 0 || len(b.Nodes) == 0 {
		return false
	}
	return a.Nodes[0] == b.Nodes[0]
}

// percent renders a share as Python's "{:.0%}" does.
func percent(share float64) string {
	return fmt.Sprintf("%.0f%%", share*100)
}
