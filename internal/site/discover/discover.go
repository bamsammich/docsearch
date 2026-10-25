// Package discover says which pages a documentation site consists of.
//
// Coverage only. How those pages nest is a different question with different
// sources, answered in internal/site/nav. Conflating them is what made an
// earlier design derive the page set from a rendered sidebar, which one
// probed site does not have and another uses to list 19 of its 210 pages.
//
// Sources contribute a union rather than competing, because each is
// incomplete in its own way and the disagreements are worth reporting:
//
//   - sitemap.xml is the most complete when present, and the authority on
//     spelling. It describes a whole site, so it is filtered to the seed's
//     path prefix; without that one probed site contributed 385 blog posts to
//     a documentation crawl.
//   - The seed page's own links are what the publisher put on the page as its
//     index. They are in scope by declaration rather than by prefix: a hub
//     page legitimately lists documents elsewhere on the host.
//   - llms.txt is ordered and titled, and not authoritative: on one probed
//     site it holds 169 of 210 pages, omitting an entire command reference.
//     It often lists each page's Markdown twin rather than the page, which
//     discovery reads as the page the twin mirrors.
//
// Both manifests are looked for beside the seed and in every directory above
// it, as well as at the origin. A host that serves documentation under a
// path keeps them there: on one probed host the origin's sitemap describes a
// marketing site, and the documentation's 1279 pages are declared only by a
// sitemap and an llms.txt under the documentation's path.
//
// Link-following is the last resort and belongs to the crawler, under a
// budget.
package discover

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/bamsammich/docsearch/internal/pystr"
	"github.com/bamsammich/docsearch/internal/site/fetch"
)

const (
	// maxSitemapDepth is where nested sitemap indexes are a loop or a
	// mistake.
	maxSitemapDepth = 3
	// resemblance is how much of the not-found template a page must hold to
	// be treated as absent. Used only against a site's own error page.
	resemblance = 0.9
	// probeAgreement is how alike two probe responses must be to count as
	// one template. Looser than resemblance on purpose: the probes differ by
	// the request echoed back, which is a large share of a short error page
	// and a small share of a long one, so one threshold would recognise
	// verbose templates and miss terse ones. What they share is intersected
	// afterwards, so a loose gate costs nothing.
	probeAgreement = 0.7
	// minSignatureTokens is the shortest template that can be told apart
	// from a short page sharing a few ordinary words.
	minSignatureTokens = 8
)

// probePaths are two improbable paths with no tokens in common. A site that
// echoes the request back puts those tokens in the body, and anything the
// probes share survives the intersection, so a shared word here would be
// mistaken for part of the template.
var probePaths = [2]string{"zqxvj7-nonexistent", "kwmbp3-missingpage"}

// word matches what Python's `[^\W_]+` matches: letters and numbers, no
// underscore.
var word = regexp.MustCompile(`[\p{L}\p{N}]+`)

// Fetcher is what discovery needs of a fetcher.
type Fetcher interface {
	Fetch(ctx context.Context, raw string, revalidate bool) (*fetch.Fetched, error)
	// Robots is the robots.txt of the host raw names.
	Robots(ctx context.Context, raw string) (string, error)
}

// Coverage is the page set, and where each part of it came from.
type Coverage struct {
	// Titles are keyed by URL, where a source supplied one.
	Titles map[string]string
	// URLs is the union, in a stable order.
	URLs        []string
	FromSitemap []string
	FromIndex   []string
	FromLLMsTxt []string
	Notes       []string
}

// Sources names the sources that contributed.
func (c *Coverage) Sources() []string {
	out := []string{}
	if len(c.FromSitemap) > 0 {
		out = append(out, "sitemap")
	}
	if len(c.FromIndex) > 0 {
		out = append(out, "index_page")
	}
	if len(c.FromLLMsTxt) > 0 {
		out = append(out, "llms_txt")
	}
	return out
}

// Discover assembles the page set for seed from every source that answers.
//
// revalidate false answers entirely from the fetch cache. Re-chunking an
// already-crawled site must cost nothing, and discovery is as much a part of
// that as the pages: a re-crawl that still fetches the sitemap, the llms.txt
// and both probes is not served from the cache.
func Discover(
	ctx context.Context,
	f Fetcher,
	seed string,
	seedPage *fetch.Fetched,
	revalidate bool,
) (*Coverage, error) {
	seed, err := fetch.Normalize(seed)
	if err != nil {
		return nil, err
	}
	cov := &Coverage{Titles: map[string]string{}}

	sitemap, notes, err := sitemapCandidates(ctx, f, seed, revalidate)
	if err != nil {
		return nil, err
	}
	cov.Notes = append(cov.Notes, notes...)
	cov.FromSitemap = scopedAndSorted(sitemap, seed)
	if len(sitemap) > 0 {
		cov.Notes = append(cov.Notes, fmt.Sprintf("sitemap declares %d URL(s), %d under the seed",
			len(sitemap), len(cov.FromSitemap)))
	}

	if err := cov.addIndexPage(ctx, f, seed, seedPage, revalidate); err != nil {
		return nil, err
	}
	if err := cov.addLLMsTxt(ctx, f, seed, revalidate); err != nil {
		return nil, err
	}
	cov.dropIndexNonPages()
	cov.union()
	cov.compareSources()
	return cov, nil
}

// addIndexPage adds the links the seed page declares. They are in scope by
// declaration rather than by prefix: a hub page legitimately lists documents
// elsewhere on the host.
func (c *Coverage) addIndexPage(
	ctx context.Context,
	f Fetcher,
	seed string,
	seedPage *fetch.Fetched,
	revalidate bool,
) error {
	if seedPage == nil {
		res, err := f.Fetch(ctx, seed, revalidate)
		if err != nil {
			c.Notes = append(c.Notes, fmt.Sprintf("seed page: %v", err))
			return nil
		}
		seedPage = res
	}
	if seedPage.Status != 200 {
		return nil
	}
	links, err := IndexPageCandidates(seedPage.Body, seed)
	if err != nil {
		return err
	}
	c.FromIndex = links
	return nil
}

// dropIndexNonPages removes the index page's links that name no page:
// manifests, and Markdown twins of pages another source declares. GitBook
// prints both on every page, so a crawl from its index fetched llms.txt and
// the seed's own twin, and refused each as not HTML.
//
// A twin whose page no source declares stays. A same-host link ending in .md
// can be an HTML page under that name, and only a site that follows the twin
// convention serves a page without the suffix.
func (c *Coverage) dropIndexNonPages() {
	declared := c.declared()
	c.FromIndex = slices.DeleteFunc(c.FromIndex, func(u string) bool {
		page, twin := pageOfTwin(u)
		return isManifest(u) || twin && declared[page]
	})
}

// declared is every URL a source names.
func (c *Coverage) declared() map[string]bool {
	out := map[string]bool{}
	for _, list := range [][]string{c.FromIndex, c.FromSitemap, c.FromLLMsTxt} {
		for _, u := range list {
			out[u] = true
		}
	}
	return out
}

// union orders the page set: the index page supplies the publisher's own
// ordering, the sitemap is the authority on spelling, llms.txt fills gaps.
func (c *Coverage) union() {
	seen := map[string]bool{}
	c.URLs = []string{}
	for _, list := range [][]string{c.FromIndex, c.FromSitemap, c.FromLLMsTxt} {
		for _, u := range list {
			if !seen[u] {
				seen[u] = true
				c.URLs = append(c.URLs, u)
			}
		}
	}
}

// compareSources reports where the sources disagree, rather than resolving
// it silently.
func (c *Coverage) compareSources() {
	if len(c.FromSitemap) > 0 {
		if n := countMissing(c.FromLLMsTxt, c.FromSitemap); n > 0 {
			c.Notes = append(c.Notes, fmt.Sprintf("%d llms.txt URL(s) absent from the sitemap", n))
		}
	}
	if len(c.FromLLMsTxt) > 0 {
		if n := countMissing(c.FromSitemap, c.FromLLMsTxt); n > 0 {
			c.Notes = append(c.Notes, fmt.Sprintf(
				"llms.txt omits %d page(s) the sitemap declares; it is not authoritative for coverage",
				n,
			))
		}
	}
}

// countMissing is how many of urls are absent from have.
func countMissing(urls, have []string) int {
	present := make(map[string]bool, len(have))
	for _, u := range have {
		present[u] = true
	}
	n := 0
	for _, u := range urls {
		if !present[u] {
			n++
		}
	}
	return n
}

// addLLMsTxt adds the pages llms.txt lists, with their titles.
func (c *Coverage) addLLMsTxt(ctx context.Context, f Fetcher, seed string, revalidate bool) error {
	pairs, notes, err := llmsTxtCandidates(ctx, f, seed, revalidate)
	if err != nil {
		return err
	}
	c.Notes = append(c.Notes, notes...)
	twins := 0
	for _, pair := range pairs {
		if c.addLLMsEntry(pair, seed) {
			twins++
		}
	}
	if twins > 0 {
		c.Notes = append(c.Notes, fmt.Sprintf(
			"llms.txt lists %d Markdown twin(s); each is crawled as the HTML page it mirrors",
			twins,
		))
	}
	return nil
}

// addLLMsEntry adds one llms.txt entry, when it sits under the seed, and
// reports whether the entry was a Markdown twin read as its page.
//
// The twin is replaced rather than fetched. It answers text/markdown under
// any Accept header, which the crawler refuses, and where the sitemap lists
// the page as well the crawl would hold the page twice: on one probed site
// 1284 of 1290 llms.txt entries were twins of sitemap pages.
func (c *Coverage) addLLMsEntry(pair titledURL, seed string) bool {
	u, err := fetch.Normalize(pair.url)
	if err != nil || isManifest(u) {
		return false
	}
	page, twin := pageOfTwin(u)
	if twin {
		u = page
	}
	if !InPrefixScope(u, seed) || slices.Contains(c.FromLLMsTxt, u) {
		return false
	}
	c.FromLLMsTxt = append(c.FromLLMsTxt, u)
	if _, ok := c.Titles[u]; !ok && pair.title != "" {
		c.Titles[u] = pair.title
	}
	return twin
}

// pageOfTwin is the page a Markdown twin mirrors, and whether raw is one.
//
// llmstxt.org asks a site to serve each page as Markdown at the page's URL
// with .md appended, and at index.html.md for a URL ending in a slash. raw is
// normalized, so it carries no fragment.
func pageOfTwin(raw string) (string, bool) {
	head, query, hasQuery := strings.Cut(raw, "?")
	if !strings.HasSuffix(head, ".md") {
		return "", false
	}
	head = strings.TrimSuffix(head, ".md")
	if strings.HasSuffix(head, "/index.html") {
		head = strings.TrimSuffix(head, "index.html")
	}
	if hasQuery {
		head += "?" + query
	}
	return head, true
}

// isManifest reports whether raw names an llms.txt file rather than a page.
// llms.txt links to its full-text sibling, and a section's index links to
// its own.
func isManifest(raw string) bool {
	head, _, _ := strings.Cut(raw, "?")
	name := head[strings.LastIndex(head, "/")+1:]
	return name == "llms.txt" || name == "llms-full.txt"
}

// scopedAndSorted normalizes the sitemap's URLs, keeps those under the seed,
// and sorts them.
func scopedAndSorted(urls []string, seed string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range urls {
		u, err := fetch.Normalize(raw)
		if err != nil || seen[u] || !InPrefixScope(u, seed) {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	slices.Sort(out)
	return out
}

// InPrefixScope reports whether url sits under the seed's path, on the
// seed's host.
//
// Applied to sources that describe a whole site, never to links the seed
// itself declares: those are in scope because the publisher put them there,
// and requiring the seed's prefix discards the ordinary case of an index
// page beside what it indexes.
func InPrefixScope(raw, seed string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	s, err := url.Parse(seed)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Hostname(), s.Hostname()) {
		return false
	}
	base := s.Path
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return u.Path == s.Path || strings.HasPrefix(u.Path, base)
}

// IndexPageCandidates is the same-host links a page declares, in document
// order, deduplicated. Duplicates are ordinary: a page that renders one
// navigation for desktop and another for mobile lists everything twice.
func IndexPageCandidates(body []byte, base string) ([]string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", base, err)
	}
	doc.Find("script, style").Remove()
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", base, err)
	}
	var out []string
	seen := map[string]bool{}
	doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		if u, ok := linkOf(baseURL, strings.TrimSpace(href)); ok && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	})
	return out, nil
}

// linkOf normalizes one href against the page it was found on, and reports
// whether it is a same-host page link.
func linkOf(base *url.URL, href string) (string, bool) {
	if !isPageLink(href) {
		return "", false
	}
	next, err := base.Parse(href)
	if err != nil {
		return "", false
	}
	u, err := fetch.Normalize(next.String())
	if err != nil {
		return "", false
	}
	parsed, err := url.Parse(u)
	if err != nil || !strings.EqualFold(parsed.Hostname(), base.Hostname()) {
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

// titledURL is one entry of llms.txt.
type titledURL struct {
	title string
	url   string
}

// llmsLink matches a Markdown list item holding a link, as llms.txt writes
// its entries.
var llmsLink = regexp.MustCompile(`(?m)^[` + pystr.SpaceChars + `]*[-*][` +
	pystr.SpaceChars + `]*\[([^\]]*)\]\((https?://[^)` + pystr.SpaceChars + `]+)\)`)

// llmsTxtCandidates reads every llms.txt from the seed's directory up to the
// origin, nearest first, each in the order it lists its pages. The nearest
// describes the documentation itself, so its titles win.
func llmsTxtCandidates(
	ctx context.Context,
	f Fetcher,
	seed string,
	revalidate bool,
) ([]titledURL, []string, error) {
	var out []titledURL
	var notes, read []string
	for _, dir := range manifestDirs(seed) {
		target := dir + "llms.txt"
		entries, err := readLLMsTxt(ctx, f, target, revalidate)
		if err != nil {
			notes = append(notes, fmt.Sprintf("llms.txt %s: %v", target, err))
			continue
		}
		if len(entries) > 0 {
			out = append(out, entries...)
			read = append(read, target)
		}
	}
	if len(read) > 1 {
		notes = append(notes, "llms.txt read at "+strings.Join(read, ", "))
	}
	return out, notes, nil
}

// readLLMsTxt is the entries of one llms.txt, none where it is absent.
func readLLMsTxt(
	ctx context.Context,
	f Fetcher,
	target string,
	revalidate bool,
) ([]titledURL, error) {
	res, err := f.Fetch(ctx, target, revalidate)
	if err != nil || res.Status != 200 {
		return nil, err
	}
	var out []titledURL
	for _, m := range llmsLink.FindAllStringSubmatch(pystr.ReadText(res.Body), -1) {
		out = append(out, titledURL{title: pystr.Strip(m[1]), url: m[2]})
	}
	return out, nil
}

// manifestDirs is where a seed's manifests may sit: the seed's directory and
// each directory above it, nearest first, ending at the origin. A seed path
// without a trailing slash is read as a directory, since a documentation
// root is usually spelled without one.
func manifestDirs(seed string) []string {
	u, err := url.Parse(seed)
	if err != nil {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	var out []string
	for dir := strings.TrimSuffix(u.EscapedPath(), "/"); dir != ""; dir = path.Dir(dir) {
		if dir == "/" {
			break
		}
		out = append(out, origin+dir+"/")
	}
	return append(out, origin+"/")
}

// sitemapCandidates is every URL the site's sitemaps declare, and notes
// about getting them.
func sitemapCandidates(
	ctx context.Context,
	f Fetcher,
	seed string,
	revalidate bool,
) ([]string, []string, error) {
	queue, notes, err := sitemapRoots(ctx, f, seed)
	if err != nil {
		return nil, nil, err
	}

	var pages []string
	seen := map[string]bool{}
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if seen[ref.url] || ref.depth > maxSitemapDepth {
			continue
		}
		seen[ref.url] = true
		found, nested, note := readSitemap(ctx, f, ref, revalidate)
		if note != "" {
			notes = append(notes, note)
		}
		pages = append(pages, found...)
		queue = append(queue, nested...)
	}
	return pages, notes, nil
}

// sitemapRoots is where to start reading sitemaps: a sitemap.xml in each
// directory between the seed and the origin, and then what robots.txt
// names, else the conventional location at the origin.
func sitemapRoots(ctx context.Context, f Fetcher, seed string) ([]sitemapRef, []string, error) {
	origin := originOf(seed)
	declared, err := sitemapsFromRobots(ctx, f, origin)
	if err != nil {
		return nil, nil, err
	}
	var refs []sitemapRef
	for _, dir := range manifestDirs(seed) {
		if dir != origin+"/" {
			refs = append(refs, sitemapRef{url: dir + "sitemap.xml", optional: true})
		}
	}
	if len(declared) == 0 {
		return append(refs, sitemapRef{url: origin + "/sitemap.xml"}), nil, nil
	}
	for _, d := range declared {
		refs = append(refs, sitemapRef{url: d})
	}
	return refs, []string{fmt.Sprintf("robots.txt names %d sitemap(s)", len(declared))}, nil
}

type sitemapRef struct {
	url   string
	depth int
	// optional is a location probed on speculation, whose absence is
	// ordinary and not worth a note.
	optional bool
}

// readSitemap reads one sitemap document: its pages, the sitemaps it nests,
// and a note when it could not be read.
func readSitemap(
	ctx context.Context,
	f Fetcher,
	ref sitemapRef,
	revalidate bool,
) ([]string, []sitemapRef, string) {
	res, err := f.Fetch(ctx, ref.url, revalidate)
	if err != nil {
		return nil, nil, fmt.Sprintf("sitemap %s: %v", ref.url, err)
	}
	if res.Status != 200 {
		if ref.optional {
			return nil, nil, ""
		}
		return nil, nil, fmt.Sprintf("sitemap %s: HTTP %d", ref.url, res.Status)
	}
	pages, nested := sitemapLocs(res.Body)
	refs := make([]sitemapRef, len(nested))
	for i, n := range nested {
		refs[i] = sitemapRef{url: n, depth: ref.depth + 1}
	}
	return pages, refs, ""
}

// sitemapLocs reads one sitemap's locations. The document element decides
// what they mean: a sitemapindex holds sitemaps, a urlset holds pages.
func sitemapLocs(body []byte) (pages, nested []string) {
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	r := locReader{}
	for {
		token, err := decoder.Token()
		if err != nil {
			return r.pages, r.nested
		}
		r.read(token)
	}
}

// locReader gathers a sitemap's locations as its tokens arrive.
type locReader struct {
	pages  []string
	nested []string
	// isIndex is set from the document element: a sitemapindex holds
	// sitemaps, a urlset holds pages.
	isIndex bool
	sawRoot bool
	inLoc   bool
}

func (r *locReader) read(token xml.Token) {
	switch t := token.(type) {
	case xml.StartElement:
		if !r.sawRoot {
			r.isIndex, r.sawRoot = t.Name.Local == "sitemapindex", true
		}
		r.inLoc = t.Name.Local == "loc"
	case xml.EndElement:
		r.inLoc = false
	case xml.CharData:
		r.addLoc(strings.TrimSpace(string(t)))
	}
}

func (r *locReader) addLoc(loc string) {
	if !r.inLoc || loc == "" {
		return
	}
	if r.isIndex {
		r.nested = append(r.nested, loc)
		return
	}
	r.pages = append(r.pages, loc)
}

// sitemapsFromRobots reads the sitemaps a host's robots.txt names.
func sitemapsFromRobots(ctx context.Context, f Fetcher, origin string) ([]string, error) {
	body, err := f.Robots(ctx, origin+"/")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range pystr.SplitLines(body) {
		name, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(pystr.Strip(name), "sitemap") {
			continue
		}
		if v := pystr.Strip(value); v != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
