package discover_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/site/discover"
	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/urlguard"
)

// DiscoverSuite exercises each coverage source against a served site.
type DiscoverSuite struct {
	suite.Suite
	server *httptest.Server
	routes map[string]string
	// softNotFound answers 200 with one template for every unknown path.
	softNotFound bool
}

func TestDiscover(t *testing.T) { suite.Run(t, new(DiscoverSuite)) }

func (s *DiscoverSuite) SetupTest() {
	s.routes = map[string]string{}
	s.softNotFound = false
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := s.routes[r.URL.Path]; ok {
			s.write(w, body)
			return
		}
		if s.softNotFound {
			s.write(w, "<html><body><h1>Page not found</h1><p>We could not find the page you "+
				"asked for on this documentation site. Try the search box or the index "+
				"instead.</p><p>You asked for "+r.URL.Path+"</p></body></html>")
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	s.T().Cleanup(s.server.Close)
}

func (s *DiscoverSuite) write(w http.ResponseWriter, body string) {
	_, err := w.Write([]byte(body))
	s.Require().NoError(err)
}

func (s *DiscoverSuite) fetcher() *fetch.Fetcher {
	cache, err := fetch.OpenSQLiteCache(s.T().Context(), filepath.Join(s.T().TempDir(), "cache.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(cache.Close()) })
	return fetch.New(cache, fetch.Options{Guard: allowLoopback, Interval: time.Millisecond})
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

// discover runs discovery for a seed path and returns the coverage.
func (s *DiscoverSuite) discover(seedPath string) *discover.Coverage {
	cov, err := discover.Discover(s.T().Context(), s.fetcher(), s.server.URL+seedPath, nil, true)
	s.Require().NoError(err)
	return cov
}

// paths strips the server's origin from a list of URLs.
func (s *DiscoverSuite) paths(urls []string) []string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		out = append(out, strings.TrimPrefix(u, s.server.URL))
	}
	return out
}

func (s *DiscoverSuite) urlset(paths ...string) string {
	var b strings.Builder
	b.WriteString(
		`<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`,
	)
	for _, p := range paths {
		fmt.Fprintf(&b, "<url><loc>%s%s</loc></url>", s.server.URL, p)
	}
	b.WriteString("</urlset>")
	return b.String()
}

func (s *DiscoverSuite) TestPrefixScopeKeepsTheSeedAndItsChildren() {
	seed := "https://example.com/docs"
	tests := []struct {
		url  string
		want bool
	}{
		{url: "https://example.com/docs", want: true},
		{url: "https://example.com/docs/install", want: true},
		{url: "https://example.com/docsearch", want: false},
		{url: "https://example.com/blog", want: false},
		{url: "https://other.example/docs/install", want: false},
	}
	for _, tt := range tests {
		s.Run(tt.url, func() {
			s.Equal(tt.want, discover.InPrefixScope(tt.url, seed))
		})
	}
}

func (s *DiscoverSuite) TestASitemapIsScopedToTheSeed() {
	s.routes["/sitemap.xml"] = s.urlset("/docs/a", "/blog/post")
	s.routes["/docs/"] = "<html><body><p>hub</p></body></html>"
	cov := s.discover("/docs/")
	s.Equal([]string{"/docs/a"}, s.paths(cov.FromSitemap))
	s.Contains(strings.Join(cov.Notes, "\n"), "sitemap declares 2 URL(s), 1 under the seed")
}

func (s *DiscoverSuite) TestASitemapIndexIsFollowed() {
	s.routes["/sitemap.xml"] = fmt.Sprintf(
		`<?xml version="1.0"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+
			`<sitemap><loc>%s/nested.xml</loc></sitemap></sitemapindex>`, s.server.URL)
	s.routes["/nested.xml"] = s.urlset("/docs/a", "/docs/b")
	s.routes["/docs/"] = "<html><body><p>hub</p></body></html>"
	cov := s.discover("/docs/")
	s.Equal([]string{"/docs/a", "/docs/b"}, s.paths(cov.FromSitemap))
}

func (s *DiscoverSuite) TestRobotsSitemapDirectiveIsUsed() {
	s.routes["/robots.txt"] = fmt.Sprintf(
		"User-agent: *\nAllow: /\nSitemap: %s/named.xml\n",
		s.server.URL,
	)
	s.routes["/named.xml"] = s.urlset("/docs/a")
	s.routes["/docs/"] = "<html><body><p>hub</p></body></html>"
	cov := s.discover("/docs/")
	s.Equal([]string{"/docs/a"}, s.paths(cov.FromSitemap))
	s.Contains(strings.Join(cov.Notes, "\n"), "robots.txt names 1 sitemap(s)")
}

func (s *DiscoverSuite) TestLLMsTxtContributesButDoesNotBoundCoverage() {
	s.routes["/sitemap.xml"] = s.urlset("/docs/a", "/docs/b")
	s.routes["/llms.txt"] = fmt.Sprintf("# Docs\n\n- [A page](%s/docs/a)\n- [C page](%s/docs/c)\n",
		s.server.URL, s.server.URL)
	s.routes["/docs/"] = "<html><body><p>hub</p></body></html>"
	cov := s.discover("/docs/")
	s.Equal([]string{"/docs/a", "/docs/c"}, s.paths(cov.FromLLMsTxt))
	s.Contains(s.paths(cov.URLs), "/docs/c", "llms.txt adds what the sitemap missed")
	s.Equal("A page", cov.Titles[s.server.URL+"/docs/a"])
	notes := strings.Join(cov.Notes, "\n")
	s.Contains(notes, "1 llms.txt URL(s) absent from the sitemap")
	s.Contains(notes, "llms.txt omits 1 page(s) the sitemap declares")
}

func (s *DiscoverSuite) TestTheSeedPageSuppliesCoverageWhenNothingElseDoes() {
	s.routes["/docs/"] = `<html><body><a href="/docs/a">A</a><a href="/elsewhere/b">B</a></body></html>`
	cov := s.discover("/docs/")
	// The seed's own links are in scope by declaration, prefix or not.
	s.Equal([]string{"/docs/a", "/elsewhere/b"}, s.paths(cov.FromIndex))
	s.Equal([]string{"index_page"}, cov.Sources())
}

func (s *DiscoverSuite) TestIndexPageLinksAreDedupedInDocumentOrder() {
	// A page that renders one navigation for desktop and another for mobile
	// lists everything twice.
	body := []byte(`<html><body><a href="/b">b</a><a href="/a">a</a><a href="/b">b again</a>` +
		`<a href="#frag">skip</a><a href="mailto:x@example.com">skip</a>` +
		`<a href="https://other.example/x">skip</a></body></html>`)
	got, err := discover.IndexPageCandidates(body, "https://example.com/docs/")
	s.Require().NoError(err)
	s.Equal([]string{"https://example.com/b", "https://example.com/a"}, got)
}

func (s *DiscoverSuite) TestAnHonest404NeedsNoSignature() {
	sig, err := discover.NotFoundSignature(s.T().Context(), s.fetcher(), s.server.URL, true)
	s.Require().NoError(err)
	s.False(sig.Found())
}

func (s *DiscoverSuite) TestASoft404SiteIsRecognised() {
	s.softNotFound = true
	sig, err := discover.NotFoundSignature(s.T().Context(), s.fetcher(), s.server.URL, true)
	s.Require().NoError(err)
	s.Require().True(sig.Found())

	absent, err := discover.LooksAbsent(
		[]byte("<html><body><h1>Page not found</h1><p>We could not "+
			"find the page you asked for on this documentation site. Try the search box or the "+
			"index instead.</p><p>You asked for /whatever</p></body></html>"),
		sig,
	)
	s.Require().NoError(err)
	s.True(absent)

	real, err := discover.LooksAbsent([]byte("<html><body><h1>Install</h1><p>Run the installer "+
		"and follow the prompts to set the software up on your machine.</p></body></html>"), sig)
	s.Require().NoError(err)
	s.False(real, "a real page is not the template")
}

func (s *DiscoverSuite) TestNothingIsAbsentWithoutASignature() {
	absent, err := discover.LooksAbsent([]byte("<html><body><p>anything</p></body></html>"), nil)
	s.Require().NoError(err)
	s.False(absent)
}

func (s *DiscoverSuite) TestASiteWithNoSourcesReportsAnEmptyCoverage() {
	cov := s.discover("/docs/")
	s.Empty(cov.URLs)
	s.Empty(cov.Sources())
}
