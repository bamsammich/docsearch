package site_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/site"
)

// InspectSuite covers what reconnaissance says about a crawl, over the same
// served fixture the extraction suite uses.
type InspectSuite struct {
	SiteSuite
}

func TestInspect(t *testing.T) { suite.Run(t, new(InspectSuite)) }

// inspect crawls the served fixture and reports on it.
func (s *InspectSuite) inspect() *domain.InspectReport {
	report := &domain.InspectReport{
		Target: s.server.URL + "/docs/", Format: "site", PredictedSource: "unknown",
	}
	s.Require().NoError(site.Inspect(s.crawl(), report))
	return report
}

// detail is the finding with a label, or "" where there is none.
func detail(report *domain.InspectReport, label string) string {
	for _, f := range report.Findings {
		if f.Label == label {
			return f.Detail
		}
	}
	return ""
}

// level is the level of the finding with a label.
func level(report *domain.InspectReport, label string) domain.Level {
	for _, f := range report.Findings {
		if f.Label == label {
			return f.Level
		}
	}
	return 0
}

func (s *InspectSuite) TestASiteThatCrawlsCleanlyIsNotBlocked() {
	s.fixture()
	report := s.inspect()

	s.False(report.Blocked())
	s.Equal("sidebar_dom", report.PredictedSource)
	s.Equal(domain.TierDeclared, report.PredictedTier)
	s.Require().NotNil(report.PageCount)
	s.Equal(6, *report.PageCount)
}

func (s *InspectSuite) TestCoverageNamesTheSourcesThatAnswered() {
	s.fixture()
	s.Contains(detail(s.inspect(), "coverage"), "6 page(s) fetched")
}

func (s *InspectSuite) TestABrokenLinkIsAWarningUntilItIsMostOfTheSite() {
	s.fixture()
	delete(s.routes, "/docs/faq")

	report := s.inspect()
	s.Equal(domain.LevelWarn, level(report, "reachability"))
	s.Contains(detail(report, "reachability"), "could not be fetched")
	s.False(report.Blocked(), "one page of six is not a partial index")
}

func (s *InspectSuite) TestASiteMostlyUnreachableIsBlocked() {
	// At or above the fatal share the ingest is refused rather than
	// producing an index over part of a site.
	s.fixture()
	for _, path := range []string{"/docs/faq", "/docs/api", "/docs/usage"} {
		delete(s.routes, path)
	}

	report := s.inspect()
	s.Equal(domain.LevelBlocked, level(report, "reachability"))
	s.True(report.Blocked())
}

func (s *InspectSuite) TestASiteThatAnswersNothingIsBlocked() {
	report := s.inspect()
	s.True(report.Blocked())
	s.Contains(detail(report, "coverage"), "no page of this site could be fetched")
}

func (s *InspectSuite) TestFurnitureIsReportedBeforeItIsStripped() {
	s.fixture()
	s.Contains(detail(s.inspect(), "page furniture"), "repeat across at least half the site")
}

func (s *InspectSuite) TestAPageThatIsMostlyScriptIsNamed() {
	// An empty shell that reached 'ready' is indistinguishable from a page
	// that genuinely says little, so reconnaissance names it.
	s.fixture()
	s.routes["/docs/api"] = "<html><head><title>API</title></head><body>" +
		sidebar() + "<div id=app></div><script>" +
		strings.Repeat("window.render();", 200) + "</script></body></html>"

	report := s.inspect()
	s.Equal(domain.LevelWarn, level(report, "client-rendered"))
	s.Contains(detail(report, "client-rendered"), "/docs/api")
}

func (s *InspectSuite) TestPagesNoNavigationNamesAreReported() {
	// Placed by URL path, never dropped: dropping them is how a command
	// reference goes missing from an index that reports success.
	// Linked from a page other than the seed, so neither the sidebar nor the
	// seed's own links name it and whichever source wins leaves it unplaced.
	s.fixture()
	s.page("/docs/orphan", "Orphan", "A page the sidebar never mentions.")
	s.routes["/docs/usage"] = strings.Replace(s.routes["/docs/usage"],
		"</main>", `<a href="/docs/orphan">Orphan</a></main>`, 1)

	report := s.inspect()
	s.Equal(domain.LevelWarn, level(report, "unplaced pages"))
	s.Contains(detail(report, "unplaced pages"), "nested by URL path")
}
