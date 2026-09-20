package nav_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/site/nav"
)

const base = "https://example.com/docs/"

// NavSuite checks which hierarchy source is believed, and what each one
// makes of a page set.
type NavSuite struct{ suite.Suite }

func TestNav(t *testing.T) { suite.Run(t, new(NavSuite)) }

// derive runs Derive over the URLs, under the seed page's HTML.
func (s *NavSuite) derive(seedHTML string, paths ...string) *nav.Hierarchy {
	coverage := make([]string, len(paths))
	for i, p := range paths {
		coverage[i] = "https://example.com" + p
	}
	h, err := nav.Derive(coverage, base, []byte(seedHTML))
	s.Require().NoError(err)
	return h
}

// placed is each page's section and ancestry, keyed by path.
func placed(h *nav.Hierarchy) map[string]string {
	out := map[string]string{}
	for _, p := range h.Placements {
		path := strings.TrimPrefix(p.URL, "https://example.com")
		out[path] = p.Section + " " + strings.Join(p.Ancestry, " > ")
	}
	return out
}

func (s *NavSuite) TestAHeadingInsideALinkTitlesThatLink() {
	// Cards are written as a link wrapping a heading; treating every heading
	// as a section opener gives each link the title of the one before it.
	h := s.derive(`<html><body><main>
		<h1>Guides</h1>
		<a href="/docs/install"><h3>Install</h3></a>
		<a href="/docs/usage"><h3>Usage</h3></a>
	</main></body></html>`, "/docs/install", "/docs/usage")
	s.Equal(nav.SourceIndexPage, h.Source)
	titles := map[string]string{}
	for _, p := range h.Placements {
		titles[strings.TrimPrefix(p.URL, "https://example.com/docs/")] = p.Title
	}
	s.Equal(map[string]string{"install": "Install", "usage": "Usage"}, titles)
}

func (s *NavSuite) TestHubPageSectionsBecomeAncestryAndNumbers() {
	h := s.derive(`<html><body><main>
		<h1>Getting started</h1>
		<a href="/docs/install">Install</a>
		<h1>Reference</h1>
		<a href="/docs/api">API</a>
	</main></body></html>`, "/docs/install", "/docs/api")
	s.Equal(map[string]string{
		"/docs/install": "1.1 Getting started",
		"/docs/api":     "2.1 Reference",
	}, placed(h))
}

func (s *NavSuite) TestTheMarketingNavDoesNotBecomeTheHierarchy() {
	// The container with the most same-host links wins, because a marketing
	// header is a nav too.
	h := s.derive(`<html><body>
		<nav><a href="/pricing">Pricing</a><a href="/about">About</a></nav>
		<aside class="sidebar"><ul>
			<li><a href="/docs/install">Install</a></li>
			<li><a href="/docs/usage">Usage</a></li>
			<li><a href="/docs/api">API</a></li>
		</ul></aside>
	</body></html>`, "/docs/install", "/docs/usage", "/docs/api")
	s.Equal(nav.SourceSidebar, h.Source)
	s.False(h.Inferred)
	s.Len(h.Placements, 3)
}

func (s *NavSuite) TestACollapsedSidebarIsRejectedInFavourOfURLPaths() {
	// A generator that collapses its categories renders a fragment of its
	// navigation; believing it would index a tenth of the site.
	paths := []string{"/docs/a", "/docs/b", "/docs/c", "/docs/d", "/docs/e"}
	h := s.derive(`<html><body><aside class="sidebar"><ul>
		<li><a href="/docs/a">A</a></li>
	</ul></aside></body></html>`, paths...)
	s.Equal(nav.SourceURLPath, h.Source)
	s.True(h.Inferred)
	s.Len(h.Placements, 5)
	s.Contains(strings.Join(h.Notes, "\n"), "falling back to URL path depth")
}

func (s *NavSuite) TestPagesAbsentFromTheSourceArePlacedByPath() {
	// Dropping them discarded 41 of one probed site's 210 pages.
	h := s.derive(`<html><body><aside class="sidebar"><ul>
		<li><a href="/docs/a">A</a></li>
		<li><a href="/docs/b">B</a></li>
		<li><a href="/docs/c">C</a></li>
	</ul></aside></body></html>`, "/docs/a", "/docs/b", "/docs/c", "/docs/lonely")
	s.Equal(nav.SourceSidebar, h.Source)
	s.Len(h.Placements, 4)
	s.Equal([]string{"https://example.com/docs/lonely"}, h.PlacedByPath)
	s.Contains(strings.Join(h.Notes, "\n"), "1 page(s) absent from sidebar_dom, placed by URL path")
}

func (s *NavSuite) TestEveryPageGetsExactlyOnePlacement() {
	// A navigation may list one page twice; the first position wins, since a
	// page with two section numbers is two documents to a section filter.
	h := s.derive(`<html><body><aside class="sidebar"><ul>
		<li><a href="/docs/a">A</a></li>
		<li><a href="/docs/b">B</a></li>
		<li><a href="/docs/a">A again</a></li>
	</ul></aside></body></html>`, "/docs/a", "/docs/b")
	s.Len(h.Placements, 2)
	s.Equal("1", h.ByURL()["https://example.com/docs/a"].Section)
}

func (s *NavSuite) TestURLPathsNestBelowTheSeed() {
	h := s.derive("", "/docs/cli/run", "/docs/cli/build", "/docs/intro")
	s.Equal(nav.SourceURLPath, h.Source)
	s.Equal(map[string]string{
		"/docs/cli/run":   "1.1 Cli",
		"/docs/cli/build": "1.2 Cli",
		"/docs/intro":     "2 ",
	}, placed(h))
}

func (s *NavSuite) TestNoCoverageYieldsAnEmptyHierarchy() {
	h, err := nav.Derive(nil, base, []byte("<html></html>"))
	s.Require().NoError(err)
	s.Empty(h.Placements)
	s.True(h.Inferred)
}

func (s *NavSuite) TestASidebarWithoutListsStillYieldsLinks() {
	h := s.derive(`<html><body><aside class="sidebar">
		<a href="/docs/a">A</a><a href="/docs/b">B</a>
	</aside></body></html>`, "/docs/a", "/docs/b")
	s.Equal(nav.SourceSidebar, h.Source)
	s.Equal(map[string]string{"/docs/a": "1 ", "/docs/b": "2 "}, placed(h))
}

func (s *NavSuite) TestNestedSidebarListsBecomeAncestry() {
	h := s.derive(`<html><body><aside class="sidebar"><ul>
		<li><span>Guides</span><ul>
			<li><a href="/docs/install">Install</a></li>
			<li><a href="/docs/usage">Usage</a></li>
		</ul></li>
		<li><a href="/docs/api">API</a></li>
	</ul></aside></body></html>`, "/docs/install", "/docs/usage", "/docs/api")
	s.Equal(map[string]string{
		"/docs/install": "1.1 Guides",
		"/docs/usage":   "1.2 Guides",
		"/docs/api":     "2 ",
	}, placed(h))
}

func (s *NavSuite) TestACategoryDoesNotAdoptItsFirstChildsLink() {
	// Searching descendants alone would take the child's title and URL for
	// the category, then list the child again beneath it.
	h := s.derive(`<html><body><aside class="sidebar"><ul>
		<li><ul>
			<li><a href="/docs/install">Install</a></li>
		</ul></li>
		<li><a href="/docs/api">API</a></li>
	</ul></aside></body></html>`, "/docs/install", "/docs/api")
	s.Equal("1.1", h.ByURL()["https://example.com/docs/install"].Section)
	s.Len(h.Placements, 2)
}

func (s *NavSuite) TestCoverageIsReportedForEachCandidate() {
	h := s.derive(fmt.Sprintf(`<html><body><aside class="sidebar"><ul>
		<li><a href="%s/docs/a">A</a></li>
		<li><a href="%s/docs/b">B</a></li>
	</ul></aside></body></html>`, "https://example.com", "https://example.com"),
		"/docs/a", "/docs/b")
	s.Contains(strings.Join(h.Notes, "\n"), "sidebar_dom places 100% of the page set")
}
