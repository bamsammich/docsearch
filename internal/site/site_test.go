package site_test

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

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/site"
	"github.com/bamsammich/docsearch/internal/site/crawl"
	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/urlguard"
)

// SiteSuite crawls a served documentation site and builds its extraction.
type SiteSuite struct {
	suite.Suite
	server *httptest.Server
	routes map[string]string
}

func TestSite(t *testing.T) { suite.Run(t, new(SiteSuite)) }

func (s *SiteSuite) SetupTest() {
	s.routes = map[string]string{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := s.routes[r.URL.Path]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeBody(w, body)
	}))
	s.T().Cleanup(s.server.Close)
}

// writeBody serves a page. A write failure is the client going away, which
// a test sees as a missing page rather than through the handler.
func writeBody(w http.ResponseWriter, body string) {
	if _, err := w.Write([]byte(body)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// page serves one page: a title, the sidebar every page renders, and body
// paragraphs.
func (s *SiteSuite) page(path, title string, paragraphs ...string) {
	var b strings.Builder
	fmt.Fprintf(&b, "<html><head><title>%s</title></head><body>", title)
	b.WriteString(sidebar())
	fmt.Fprintf(&b, "<main><h1>%s</h1>", title)
	for _, p := range paragraphs {
		fmt.Fprintf(&b, "<p>%s</p>", p)
	}
	b.WriteString("</main></body></html>")
	s.routes[path] = b.String()
}

// sidebar is the navigation every page of the fixture renders.
func sidebar() string {
	return `<aside class="sidebar"><ul>` +
		`<li><a href="/docs/">Docs</a></li>` +
		`<li><a href="/docs/install">Install</a></li>` +
		`<li><a href="/docs/usage">Usage</a></li>` +
		`<li><a href="/docs/api">API</a></li>` +
		`<li><a href="/docs/faq">FAQ</a></li>` +
		`<li><a href="/docs/changes">Changes</a></li>` +
		`</ul></aside>`
}

// crawl walks the served fixture.
func (s *SiteSuite) crawl() *crawl.Result {
	cache, err := fetch.OpenSQLiteCache(s.T().Context(), filepath.Join(s.T().TempDir(), "cache.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(cache.Close()) })
	f := fetch.New(cache, fetch.Options{Guard: allowLoopback, Interval: time.Millisecond})

	result, err := crawl.Crawl(
		s.T().Context(),
		f,
		s.server.URL+"/docs/",
		crawl.Options{Revalidate: true},
	)
	s.Require().NoError(err)
	return result
}

func (s *SiteSuite) extraction() *domain.Extraction {
	ext, err := site.BuildExtraction(s.crawl(), "")
	s.Require().NoError(err)
	return ext
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

// fixture serves a five-page site under a seed that lists every page.
func (s *SiteSuite) fixture() {
	s.page("/docs/", "Docs", "Everything about running and configuring the software.")
	s.page("/docs/install", "Install", "Run the installer and follow the prompts.")
	s.page("/docs/usage", "Usage", "Start the program and open the main window.")
	s.page("/docs/api", "API", "Every endpoint answers JSON over HTTPS.")
	s.page("/docs/faq", "FAQ", "Common questions about the software and its licence.")
	s.page("/docs/changes", "Changes", "Release notes for each published version.")
}

func (s *SiteSuite) TestASiteBecomesOneDocument() {
	s.fixture()
	ext := s.extraction()
	s.Equal("site", ext.Format)
	s.Equal("Docs", ext.Title, "the seed page's title names the site")
	s.NotEmpty(ext.Blocks)
	s.Equal("sidebar_dom", ext.Diagnostics["structure_source"])
}

func (s *SiteSuite) TestEveryPageIsAnAuthoritativeSection() {
	s.fixture()
	ext := s.extraction()
	sections := map[string]string{}
	for _, b := range ext.Blocks {
		s.Require().NotNil(b.Section)
		s.Require().NotNil(b.URL)
		sections[strings.TrimPrefix(*b.URL, s.server.URL)] = *b.Section
	}
	s.Equal(map[string]string{
		"/docs/":        "1",
		"/docs/install": "2",
		"/docs/usage":   "3",
		"/docs/api":     "4",
		"/docs/faq":     "5",
		"/docs/changes": "6",
	}, sections)
}

func (s *SiteSuite) TestChunksCarryTheAddressTheyWereReadFrom() {
	s.fixture()
	chunks := domain.Chunks(*s.extraction())
	s.Require().NotEmpty(chunks)
	for _, c := range chunks {
		s.Require().NotNil(c.URL)
		s.Contains(*c.URL, s.server.URL+"/docs/")
	}
}

func (s *SiteSuite) TestThePageTitleIsNotRepeatedInTheHeadingPath() {
	s.fixture()
	ext := s.extraction()
	for _, b := range ext.Blocks {
		path := strings.Join(b.HeadingPath, " > ")
		s.NotContains(path, "Install > Install", path)
		s.NotContains(path, "Usage > Usage", path)
	}
}

func (s *SiteSuite) TestASidebarRepeatedOnEveryPageIsStripped() {
	s.fixture()
	ext := s.extraction()
	for _, b := range ext.Blocks {
		s.NotEqual("Install", b.Text, "a navigation label is furniture, not content")
		s.NotEqual("FAQ", b.Text)
	}
	stats, ok := ext.Diagnostics["site"].(map[string]any)
	s.Require().True(ok)
	s.Positive(stats["chrome_blocks_dropped"])
}

func (s *SiteSuite) TestContentAppearingOnACoupleOfPagesIsNotChrome() {
	s.fixture()
	shared := "This paragraph appears on two pages and is content, not furniture."
	s.page("/docs/install", "Install", shared)
	s.page("/docs/usage", "Usage", shared)
	ext := s.extraction()
	kept := 0
	for _, b := range ext.Blocks {
		if b.Text == shared {
			kept++
		}
	}
	s.Equal(2, kept)
}

func (s *SiteSuite) TestASmallSiteIsNeverChromeStripped() {
	// Below five pages, repetition is not evidence.
	s.page("/docs/", "Docs", "Everything about the software.")
	s.page("/docs/install", "Install", "Run the installer.")
	s.page("/docs/usage", "Usage", "Open the main window.")
	ext := s.extraction()
	labels := 0
	for _, b := range ext.Blocks {
		if b.Text == "Install" || b.Text == "Usage" {
			labels++
		}
	}
	s.Positive(labels, "the sidebar survives on a site too small to judge")
}

func (s *SiteSuite) TestAFewBrokenLinksAreReportedNotFatal() {
	s.fixture()
	delete(s.routes, "/docs/faq")
	ext := s.extraction()
	stats, ok := ext.Diagnostics["site"].(map[string]any)
	s.Require().True(ok)
	s.Len(stats["unreachable"].([]string), 1)
	s.Equal(6, stats["pages_declared"])
	s.Equal(5, stats["pages_fetched"])
}
