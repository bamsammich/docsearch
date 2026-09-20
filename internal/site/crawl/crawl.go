// Package crawl visits a documentation site: the frontier, and what bounds
// it.
//
// internal/site/discover says which pages exist and internal/site/nav says
// how they nest. This visits them, and it is its own package because the
// bounds are the hard part: a seed that addresses far more than a manual, a
// site that answers 200 for everything, a crawl killed halfway through. None
// of those are questions about discovery or hierarchy.
//
// The frontier is the coverage set, scoped. The path prefix is a containment
// check applied to discovered links, never the source of them: one probed
// site's seed sits under one path while every document sits under another,
// so deriving the frontier from the seed's prefix yields nothing at all.
//
// Link-following is the only part that generates URLs rather than filtering
// them. It runs only when no site-wide manifest answered, and then under a
// depth and a page budget. Links are harvested from pages as they are
// fetched, so no page is requested twice and the known-page count rises as
// the crawl proceeds.
//
// Ported from python/docsearch/crawl.py.
package crawl

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/bamsammich/docsearch/internal/site/discover"
	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/site/nav"
)

const (
	// DefaultMaxPages is how many pages one crawl will fetch, a backstop
	// against a seed that addresses a whole site rather than one manual. The
	// fetcher's own budget sits below it as a floor under a bug.
	DefaultMaxPages = 500
	// DefaultLinkDepth is how far link-following walks from the seed. It is
	// reached only when no manifest answered, at which point there is no
	// declared structure to trust and depth is the only bound left.
	DefaultLinkDepth = 3
)

// htmlTypes are the content types worth extracting. A documentation site
// that serves a PDF at a URL is a file ingest wearing a URL, and the
// adapters pick by suffix on a local path rather than by content type.
var htmlTypes = []string{"text/html", "application/xhtml+xml"}

// Page is one fetched page of the site.
type Page struct {
	URL      string
	FinalURL string
	// DeclaredTitle is the title a coverage source supplied, where one did.
	// The page's own h1 is usually better, but that belongs to extraction.
	DeclaredTitle string
	Body          []byte
}

// Pair is two URLs: a duplicate and the URL it declares as canonical, or a
// page and the reason it could not be read.
type Pair struct {
	URL    string
	Reason string
}

// Result is what a crawl visited, and everything it could not.
type Result struct {
	Coverage *discover.Coverage
	// Hierarchy places every page the crawl fetched.
	Hierarchy *nav.Hierarchy
	// Pages are keyed by normalized URL, in the order the frontier visited.
	Pages map[string]*Page
	Seed  string
	// Order is the visiting order of Pages.
	Order []string
	// Unreachable is every declared page that produced no content.
	Unreachable []Pair
	// CanonicalMerges are the URLs collapsed into the one they name.
	CanonicalMerges []Pair
	Notes           []string
}

// Declared is how many pages the crawl knew about: fetched, plus those it
// could not reach.
func (r *Result) Declared() int { return len(r.Pages) + len(r.Unreachable) }

// UnreachableShare is the share of known pages that produced nothing. The
// completeness gate reads it: a broken link on a large site is ordinary, and
// a large share of them means the site was not navigable and the index would
// be a silent partial.
func (r *Result) UnreachableShare() float64 {
	if r.Declared() == 0 {
		return 0
	}
	return float64(len(r.Unreachable)) / float64(r.Declared())
}

// Phase names the stage a progress report describes.
type Phase uint8

const (
	// PhaseDiscover is asking every coverage source which pages exist.
	PhaseDiscover Phase = iota + 1
	// PhaseFetch is walking the frontier.
	PhaseFetch
)

// Progress reports how far a crawl has got.
//
// The total rises as link-following discovers more pages, which is the honest
// figure: nothing knows a walked site's page count up front, and a total that
// only ever grew from a guess would read as a crawl going backwards.
type Progress func(phase Phase, current, total int)

// report calls a progress function that may be absent.
func (p Progress) report(phase Phase, current, total int) {
	if p != nil {
		p(phase, current, total)
	}
}

// Options bound one crawl.
type Options struct {
	// Progress is called as discovery and the walk advance. Nil reports
	// nothing, which is what a caller with nobody watching wants.
	Progress  Progress
	MaxPages  int
	LinkDepth int
	// Revalidate false serves the whole crawl from the fetch cache without a
	// single request, which is what re-chunking an already-crawled site
	// wants.
	Revalidate bool
}

// Crawl fetches every page the seed covers and reports what it could not
// reach.
func Crawl(ctx context.Context, f discover.Fetcher, seed string, opts Options) (*Result, error) {
	seed, err := fetch.Normalize(seed)
	if err != nil {
		return nil, err
	}
	if opts.MaxPages == 0 {
		opts.MaxPages = DefaultMaxPages
	}
	if opts.LinkDepth == 0 {
		opts.LinkDepth = DefaultLinkDepth
	}
	c := &crawler{
		fetcher: f, seed: seed, opts: opts,
		result: &Result{Seed: seed, Pages: map[string]*Page{}},
	}
	if err := c.run(ctx); err != nil {
		return nil, err
	}
	return c.result, nil
}

// crawler holds one crawl's state.
type crawler struct {
	fetcher   discover.Fetcher
	result    *Result
	signature *discover.Signature
	known     map[string]bool
	seed      string
	queue     []frontierEntry
	opts      Options
	followed  bool
}

type frontierEntry struct {
	url   string
	depth int
}

func (c *crawler) run(ctx context.Context) error {
	c.opts.Progress.report(PhaseDiscover, 0, 0)
	seedPage := c.fetchSeed(ctx)
	cov, err := discover.Discover(ctx, c.fetcher, c.seed, seedPage, c.opts.Revalidate)
	if err != nil {
		return err
	}
	c.result.Coverage = cov
	c.result.Notes = append(c.result.Notes, cov.Notes...)

	// A manifest describes the whole site, so its absence is what licenses
	// walking links. With one present, the declared set is the frontier.
	c.followed = len(cov.FromSitemap) == 0 && len(cov.FromLLMsTxt) == 0
	if c.followed {
		c.note(
			"no sitemap or llms.txt; following links from fetched pages to depth %d",
			c.opts.LinkDepth,
		)
	}
	c.start(cov.URLs)

	// Computed before the frontier is walked: a site that answers 200 for
	// everything would otherwise feed its error page to the index, and the
	// completeness gate would score that a success.
	c.signature, err = discover.NotFoundSignature(
		ctx,
		c.fetcher,
		originOf(c.seed),
		c.opts.Revalidate,
	)
	if err != nil {
		return err
	}
	if c.signature.Found() {
		c.note("this site answers 200 for pages that do not exist; responses matching its " +
			"not-found template are treated as unreachable")
	}
	if err := c.walk(ctx); err != nil {
		return err
	}
	c.summarize()
	// Derived against what was fetched rather than what was declared: a
	// hierarchy source that places pages the crawl never got is not placing
	// anything a caller can reach.
	hierarchy, err := nav.Derive(c.result.Order, c.seed, seedBody(seedPage))
	if err != nil {
		return err
	}
	c.result.Hierarchy = hierarchy
	c.result.Notes = append(c.result.Notes, hierarchy.Notes...)
	return nil
}

// seedBody is the seed page's body, or nil where the seed could not be read.
func seedBody(seedPage *fetch.Fetched) []byte {
	if seedPage == nil {
		return nil
	}
	return seedPage.Body
}

// fetchSeed reads the seed page, which discovery reads as the index page.
func (c *crawler) fetchSeed(ctx context.Context) *fetch.Fetched {
	res, err := c.fetcher.Fetch(ctx, c.seed, c.opts.Revalidate)
	if err != nil {
		c.note("seed %s: %v", c.seed, err)
		return nil
	}
	if res.Status != 200 {
		c.note("seed %s: HTTP %d", c.seed, res.Status)
		return nil
	}
	return res
}

// start fills the frontier from the coverage set, within the page budget.
func (c *crawler) start(urls []string) {
	frontier := urls
	if len(frontier) > c.opts.MaxPages {
		for _, dropped := range frontier[c.opts.MaxPages:] {
			c.result.Unreachable = append(c.result.Unreachable, Pair{
				URL: dropped, Reason: fmt.Sprintf("page budget of %d exhausted", c.opts.MaxPages),
			})
		}
		c.note("page budget of %d reached; %d declared page(s) were not fetched and are "+
			"reported as unreachable", c.opts.MaxPages, len(frontier)-c.opts.MaxPages)
		frontier = frontier[:c.opts.MaxPages]
	}
	c.known = make(map[string]bool, len(frontier))
	for _, u := range frontier {
		c.known[u] = true
		c.queue = append(c.queue, frontierEntry{url: u})
	}
}

// walk visits the frontier, adding links from each page when no manifest
// bounded the crawl.
func (c *crawler) walk(ctx context.Context) error {
	done := 0
	for len(c.queue) > 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("crawl %s: %w", c.seed, err)
		}
		c.opts.Progress.report(PhaseFetch, done, len(c.known))
		done++
		entry := c.queue[0]
		c.queue = c.queue[1:]
		if err := c.visit(ctx, entry); err != nil {
			return err
		}
	}
	c.opts.Progress.report(PhaseFetch, done, len(c.known))
	return nil
}

// visit reads one page of the frontier and keeps it, when it holds content.
func (c *crawler) visit(ctx context.Context, entry frontierEntry) error {
	if _, done := c.result.Pages[entry.url]; done {
		return nil
	}
	body, ok, err := c.read(ctx, entry.url)
	if err != nil || !ok {
		return err
	}
	return c.keep(entry, body)
}

// read fetches one page and reports whether it holds content, recording why
// when it does not.
func (c *crawler) read(ctx context.Context, raw string) (*fetch.Fetched, bool, error) {
	res, err := c.fetcher.Fetch(ctx, raw, c.opts.Revalidate)
	if err != nil {
		c.unreachable(raw, err.Error())
		return nil, false, nil
	}
	switch {
	case res.Status != 200:
		c.unreachable(raw, fmt.Sprintf("HTTP %d", res.Status))
		return nil, false, nil
	case !isHTML(res.ContentType):
		c.unreachable(raw, "not HTML: "+res.ContentType)
		return nil, false, nil
	}
	absent, err := discover.LooksAbsent(res.Body, c.signature)
	if err != nil {
		return nil, false, err
	}
	if absent {
		c.unreachable(raw, "soft 404: matches the site's not-found template")
		return nil, false, nil
	}
	return res, true, nil
}

// keep stores a page under the URL it declares as canonical, and queues the
// links it declares.
func (c *crawler) keep(entry frontierEntry, res *fetch.Fetched) error {
	key := entry.url
	canonical, err := canonicalOf(res.Body, entry.url)
	if err != nil {
		return err
	}
	if canonical != "" && canonical != entry.url && discover.InPrefixScope(canonical, c.seed) {
		// The publisher says this is one page under two spellings. Keep the
		// one it named, so a versioned site is not indexed once per version.
		c.result.CanonicalMerges = append(
			c.result.CanonicalMerges,
			Pair{URL: entry.url, Reason: canonical},
		)
		if _, done := c.result.Pages[canonical]; done {
			return nil
		}
		key = canonical
		c.known[canonical] = true
	}
	c.result.Pages[key] = &Page{
		URL: key, FinalURL: res.FinalURL, Body: res.Body,
		DeclaredTitle: c.result.Coverage.Titles[entry.url],
	}
	c.result.Order = append(c.result.Order, key)
	if !c.followed || entry.depth >= c.opts.LinkDepth {
		return nil
	}
	return c.queueLinks(res.Body, entry)
}

// queueLinks adds the same-host links a page declares, within scope and
// within the budget.
func (c *crawler) queueLinks(body []byte, entry frontierEntry) error {
	links, err := discover.IndexPageCandidates(body, entry.url)
	if err != nil {
		return err
	}
	for _, link := range links {
		if len(c.known) >= c.opts.MaxPages {
			return nil
		}
		// Scope applies to discovered links, never to declared ones.
		if c.known[link] || !discover.InPrefixScope(link, c.seed) {
			continue
		}
		c.known[link] = true
		c.queue = append(c.queue, frontierEntry{url: link, depth: entry.depth + 1})
	}
	return nil
}

func (c *crawler) summarize() {
	if n := len(c.result.CanonicalMerges); n > 0 {
		c.note("%d page(s) collapsed into the URL they declare as canonical", n)
	}
	if n := len(c.result.Unreachable); n > 0 {
		c.note("%d of %d known page(s) could not be fetched (%s)",
			n, c.result.Declared(), percent(c.result.UnreachableShare()))
	}
}

func (c *crawler) unreachable(raw, reason string) {
	c.result.Unreachable = append(c.result.Unreachable, Pair{URL: raw, Reason: reason})
}

func (c *crawler) note(format string, args ...any) {
	c.result.Notes = append(c.result.Notes, fmt.Sprintf(format, args...))
}

// percent renders a share as Python's "{:.0%}" does.
func percent(share float64) string {
	return fmt.Sprintf("%.0f%%", share*100)
}

// isHTML reports whether a response is worth extracting. A server that
// declares nothing gets the benefit of the doubt: refusing an unlabelled
// response would drop pages from generators that serve static files with no
// type.
func isHTML(contentType string) bool {
	if contentType == "" {
		return true
	}
	head, _, _ := strings.Cut(contentType, ";")
	return slicesContainsFold(htmlTypes, strings.TrimSpace(head))
}

func slicesContainsFold(list []string, want string) bool {
	for _, item := range list {
		if strings.EqualFold(item, want) {
			return true
		}
	}
	return false
}

// canonicalOf is the URL a page declares as its own, if it declares one.
//
// Versioned documentation produces duplicates whenever a seed spans a
// "latest" path and a numbered one, and a canonical link is the publisher
// saying which spelling is the page.
func canonicalOf(body []byte, base string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", base, err)
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", base, err)
	}
	found := ""
	doc.Find("link[href]").EachWithBreak(func(_ int, link *goquery.Selection) bool {
		u, ok := canonicalHref(baseURL, link)
		if ok {
			found = u
		}
		return !ok
	})
	return found, nil
}

// canonicalHref is the normalized target of one link element, when the link
// is the page's canonical one.
func canonicalHref(base *url.URL, link *goquery.Selection) (string, bool) {
	rel, _ := link.Attr("rel")
	if !slicesContainsFold(strings.Fields(rel), "canonical") {
		return "", false
	}
	href, _ := link.Attr("href")
	if href = strings.TrimSpace(href); href == "" {
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
	return u, true
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
