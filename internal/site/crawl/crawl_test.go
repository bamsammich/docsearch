package crawl_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/site/crawl"
	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/urlguard"
)

// CrawlSuite crawls a documentation site served on an ephemeral port, as the
// Python tests do.
type CrawlSuite struct {
	suite.Suite
	server *httptest.Server
	// routes are served as they are; a path not named answers 404.
	routes map[string]route
	// softNotFound answers 200 with one template for every unknown path.
	softNotFound bool
	requests     atomic.Int64
}

// route is one served response.
type route struct {
	contentType string
	body        string
}

func TestCrawl(t *testing.T) { suite.Run(t, new(CrawlSuite)) }

func (s *CrawlSuite) SetupTest() {
	s.routes = map[string]route{}
	s.softNotFound = false
	s.requests.Store(0)
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		if got, ok := s.routes[r.URL.Path]; ok {
			w.Header().Set("Content-Type", or(got.contentType, "text/html"))
			s.write(w, got.body)
			return
		}
		if s.softNotFound {
			// One template, plus the path echoed back.
			w.Header().Set("Content-Type", "text/html")
			s.write(w, "<html><body><h1>Page not found</h1><p>We could not find the page "+
				"you asked for on this documentation site. Try the search box or the "+
				"index.</p><p>You asked for "+r.URL.Path+"</p></body></html>")
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	s.T().Cleanup(s.server.Close)
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func (s *CrawlSuite) write(w http.ResponseWriter, body string) {
	_, err := w.Write([]byte(body))
	s.Require().NoError(err)
}

// page adds an HTML page with the links it declares.
func (s *CrawlSuite) page(path, title string, links ...string) {
	var b strings.Builder
	fmt.Fprintf(&b, "<html><head><title>%s</title></head><body><h1>%s</h1>", title, title)
	for _, link := range links {
		fmt.Fprintf(&b, `<a href="%s">%s</a>`, link, link)
	}
	b.WriteString("<p>Body text for the page, long enough to be prose.</p></body></html>")
	s.routes[path] = route{body: b.String()}
}

// crawler returns a crawler over a fetcher with its own cache, and the
// server's URL for the seed.
func (s *CrawlSuite) fetcher() *fetch.Fetcher {
	cache, err := fetch.OpenSQLiteCache(s.T().Context(), filepath.Join(s.T().TempDir(), "cache.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(cache.Close()) })
	return fetch.New(cache, fetch.Options{
		Guard:        allowLoopback,
		IgnoreRobots: false,
		Interval:     time.Millisecond,
	})
}

func allowLoopback(_ context.Context, raw string) (*urlguard.Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil {
		return nil, err
	}
	return &urlguard.Target{URL: u, Addrs: []netip.Addr{addr}}, nil
}

func (s *CrawlSuite) crawl(seedPath string, opts crawl.Options) *crawl.Result {
	res, err := crawl.Crawl(s.T().Context(), s.fetcher(), s.server.URL+seedPath, opts)
	s.Require().NoError(err)
	return res
}

// paths is the crawled pages' paths, in visiting order.
func (s *CrawlSuite) paths(res *crawl.Result) []string {
	out := make([]string, 0, len(res.Order))
	for _, u := range res.Order {
		out = append(out, strings.TrimPrefix(u, s.server.URL))
	}
	return out
}

func (s *CrawlSuite) TestEveryDeclaredPageIsFetched() {
	s.page("/docs/", "Guide", "/docs/install", "/docs/usage")
	s.page("/docs/install", "Install")
	s.page("/docs/usage", "Usage")
	s.routes["/sitemap.xml"] = route{contentType: "application/xml", body: s.sitemap(
		"/docs/", "/docs/install", "/docs/usage")}

	got := s.crawl("/docs/", crawl.Options{Revalidate: true})
	s.ElementsMatch([]string{"/docs/", "/docs/install", "/docs/usage"}, s.paths(got))
	s.Empty(got.Unreachable)
	s.Equal([]string{"sitemap", "index_page"}, sortedSources(got.Coverage.Sources()))
}

func (s *CrawlSuite) TestThePageBudgetCapsTheCrawlAndReportsTheRemainder() {
	s.page("/docs/", "Guide", "/docs/a", "/docs/b", "/docs/c")
	for _, p := range []string{"/docs/a", "/docs/b", "/docs/c"} {
		s.page(p, p)
	}
	s.routes["/sitemap.xml"] = route{contentType: "application/xml", body: s.sitemap(
		"/docs/", "/docs/a", "/docs/b", "/docs/c")}

	got := s.crawl("/docs/", crawl.Options{Revalidate: true, MaxPages: 2})
	s.Len(got.Pages, 2)
	s.Len(got.Unreachable, 2)
	s.Contains(got.Unreachable[0].Reason, "page budget of 2 exhausted")
	s.Contains(strings.Join(got.Notes, "\n"), "page budget of 2 reached")
}

func (s *CrawlSuite) TestAMissingPageIsReportedNotFatal() {
	// The seed is crawled only where a source declares it, so the sitemap
	// names it beside the page that is gone.
	s.page("/docs/", "Guide", "/docs/gone")
	s.routes["/sitemap.xml"] = route{
		contentType: "application/xml",
		body:        s.sitemap("/docs/", "/docs/gone"),
	}
	got := s.crawl("/docs/", crawl.Options{Revalidate: true})
	s.Len(got.Pages, 1)
	s.Require().Len(got.Unreachable, 1)
	s.Equal("HTTP 404", got.Unreachable[0].Reason)
	s.InDelta(0.5, got.UnreachableShare(), 0.001)
}

func (s *CrawlSuite) TestANonHTMLPageIsSkippedWithItsReason() {
	s.page("/docs/", "Guide", "/docs/manual.pdf")
	s.routes["/docs/manual.pdf"] = route{contentType: "application/pdf", body: "%PDF-1.7"}
	s.routes["/sitemap.xml"] = route{
		contentType: "application/xml",
		body:        s.sitemap("/docs/", "/docs/manual.pdf"),
	}
	got := s.crawl("/docs/", crawl.Options{Revalidate: true})
	s.Len(got.Pages, 1)
	s.Require().Len(got.Unreachable, 1)
	s.Contains(got.Unreachable[0].Reason, "not HTML: application/pdf")
}

func (s *CrawlSuite) TestASoft404PageIsTreatedAsUnreachable() {
	s.softNotFound = true
	s.page("/docs/", "Guide", "/docs/real", "/docs/imaginary")
	s.page("/docs/real", "Real")
	got := s.crawl("/docs/", crawl.Options{Revalidate: true})
	s.Equal([]string{"/docs/real"}, s.paths(got))
	s.Require().Len(got.Unreachable, 1)
	s.Contains(got.Unreachable[0].Reason, "soft 404")
	s.Contains(strings.Join(got.Notes, "\n"), "answers 200 for pages that do not exist")
}

func (s *CrawlSuite) TestACanonicalLinkCollapsesAVersionedDuplicate() {
	s.page("/docs/", "Guide", "/docs/latest/page", "/docs/v2/page")
	canonical := s.server.URL + "/docs/v2/page"
	s.routes["/docs/latest/page"] = route{body: fmt.Sprintf(
		`<html><head><link rel="canonical" href="%s"></head><body><p>one page</p></body></html>`,
		canonical)}
	s.routes["/docs/v2/page"] = route{body: `<html><body><p>one page</p></body></html>`}

	got := s.crawl("/docs/", crawl.Options{Revalidate: true})
	// The seed declares both spellings; the crawl keeps the canonical one.
	s.Equal([]string{"/docs/v2/page"}, s.paths(got))
	s.Require().Len(got.CanonicalMerges, 1)
	s.Equal(canonical, got.CanonicalMerges[0].Reason)
}

func (s *CrawlSuite) TestLinksAreFollowedWhenNoManifestAnswers() {
	s.page("/docs/", "Guide", "/docs/one")
	s.page("/docs/one", "One", "/docs/two")
	s.page("/docs/two", "Two")
	got := s.crawl("/docs/", crawl.Options{Revalidate: true})
	s.ElementsMatch([]string{"/docs/one", "/docs/two"}, s.paths(got))
	s.Contains(strings.Join(got.Notes, "\n"), "no sitemap or llms.txt")
}

func (s *CrawlSuite) TestLinkFollowingRespectsTheDepthBound() {
	s.page("/docs/", "Guide", "/docs/one")
	s.page("/docs/one", "One", "/docs/two")
	s.page("/docs/two", "Two", "/docs/three")
	s.page("/docs/three", "Three")
	got := s.crawl("/docs/", crawl.Options{Revalidate: true, LinkDepth: 1})
	s.ElementsMatch([]string{"/docs/one", "/docs/two"}, s.paths(got),
		"the seed's own links are depth 0, and one hop past them is depth 1")
}

func (s *CrawlSuite) TestDiscoveredLinksAreScopedButDeclaredOnesAreNot() {
	// The seed's own links are in scope by declaration; links found deeper
	// are held to the seed's prefix.
	s.page("/hub", "Hub", "/docs/one", "/blog/post")
	s.page("/docs/one", "One", "/blog/other")
	s.page("/blog/post", "Post")
	s.page("/blog/other", "Other")
	got := s.crawl("/hub", crawl.Options{Revalidate: true})
	s.ElementsMatch([]string{"/docs/one", "/blog/post"}, s.paths(got),
		"/blog/other was found on a page, not declared by the seed, so the prefix applies")
}

func (s *CrawlSuite) TestASitemapSuppressesLinkFollowing() {
	s.page("/docs/", "Guide", "/docs/linked")
	s.page("/docs/linked", "Linked")
	s.routes["/sitemap.xml"] = route{contentType: "application/xml", body: s.sitemap("/docs/")}
	got := s.crawl("/docs/", crawl.Options{Revalidate: true})
	// The seed page's own links still count as coverage; nothing deeper is
	// walked, and the note about link-following is absent.
	s.NotContains(strings.Join(got.Notes, "\n"), "no sitemap or llms.txt")
}

func (s *CrawlSuite) TestASecondCrawlCanRunEntirelyFromTheCache() {
	s.page("/docs/", "Guide", "/docs/one")
	s.page("/docs/one", "One")
	cache, err := fetch.OpenSQLiteCache(s.T().Context(), filepath.Join(s.T().TempDir(), "cache.db"))
	s.Require().NoError(err)
	defer func() { s.Require().NoError(cache.Close()) }()
	f := fetch.New(cache, fetch.Options{Guard: allowLoopback, Interval: time.Millisecond})

	first, err := crawl.Crawl(
		s.T().Context(),
		f,
		s.server.URL+"/docs/",
		crawl.Options{Revalidate: true},
	)
	s.Require().NoError(err)
	s.Len(first.Pages, 1)

	before := s.requests.Load()
	second, err := crawl.Crawl(s.T().Context(), f, s.server.URL+"/docs/", crawl.Options{})
	s.Require().NoError(err)
	s.Len(second.Pages, 1)
	s.Equal(before, s.requests.Load(), "a re-crawl from the cache asks the server nothing")
}

func (s *CrawlSuite) TestAnUnreachableSeedYieldsAnEmptyCrawl() {
	got := s.crawl("/docs/", crawl.Options{Revalidate: true})
	s.Empty(got.Pages)
	s.Contains(strings.Join(got.Notes, "\n"), "HTTP 404")
}

func (s *CrawlSuite) TestCancellationStopsTheCrawl() {
	s.page("/docs/", "Guide", "/docs/one", "/docs/two")
	s.page("/docs/one", "One")
	s.page("/docs/two", "Two")
	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()
	_, err := crawl.Crawl(ctx, s.fetcher(), s.server.URL+"/docs/", crawl.Options{Revalidate: true})
	s.Require().ErrorIs(err, context.Canceled)
}

// sitemap renders a sitemap naming the paths.
func (s *CrawlSuite) sitemap(paths ...string) string {
	var b strings.Builder
	b.WriteString(
		`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`,
	)
	for _, p := range paths {
		fmt.Fprintf(&b, "<url><loc>%s%s</loc></url>", s.server.URL, p)
	}
	b.WriteString("</urlset>")
	return b.String()
}

func sortedSources(sources []string) []string {
	out := slices.Clone(sources)
	slices.Sort(out)
	slices.Reverse(out)
	return out
}
