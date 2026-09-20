package site

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/site/crawl"
	"github.com/bamsammich/docsearch/internal/site/nav"
)

const (
	// clientRenderedMaxTextChars is the extractable text below which a page
	// carrying far more script than text is rendered in the browser.
	// Ingesting one stores an empty shell that looks like a successful page.
	clientRenderedMaxTextChars = 200
	// clientRenderedScriptRatio is script characters per character of text,
	// above which the page is mostly application rather than document.
	clientRenderedScriptRatio = 4.0
)

// Inspect reports what ingest would make of a crawl, writing nothing.
//
// A live dry run rather than a prediction: every question worth asking about
// a site, how many pages there are and whether a navigation places them and
// whether they carry text at all, is a question about what the server
// actually returns.
func Inspect(result *crawl.Result, report *domain.InspectReport) error {
	pages := len(result.Pages)
	report.PageCount = &pages
	if pages == 0 {
		report.Add(domain.LevelBlocked, "coverage",
			"no page of this site could be fetched. "+
				orDefault(strings.Join(result.Notes, "; "),
					"nothing answered at the seed URL."))
		return nil
	}

	coverage(result, report)
	reachability(result, report)
	hierarchy(result, report)
	shells, err := clientRendered(result)
	if err != nil {
		return err
	}
	if len(shells) > 0 {
		report.Add(domain.LevelWarn, "client-rendered", fmt.Sprintf(
			"%d page(s) carry far more script than text and are rendered in the browser. "+
				"Ingesting them stores an empty shell that looks like a successful page. "+
				"First few: %s", len(shells), strings.Join(first(shells, 3), ", ")))
	}
	return furniture(result, report)
}

// coverage reports which sources said the pages exist.
func coverage(result *crawl.Result, report *domain.InspectReport) {
	sources := result.Coverage.Sources()
	level, named := domain.LevelOK, strings.Join(sources, ", ")
	if len(sources) == 0 {
		level, named = domain.LevelWarn, "none; the page set came from following links"
	}
	report.Add(level, "coverage", fmt.Sprintf(
		"%d page(s) fetched. Sources that answered: %s.", len(result.Pages), named))
}

// reachability reports the pages a source declared and the crawl never got.
func reachability(result *crawl.Result, report *domain.InspectReport) {
	if len(result.Unreachable) == 0 {
		return
	}
	share := result.UnreachableShare()
	level := domain.LevelWarn
	if share >= domain.SiteIncompleteFatalShare {
		level = domain.LevelBlocked
	}
	var why []string
	for _, u := range result.Unreachable[:min(len(result.Unreachable), 3)] {
		why = append(why, fmt.Sprintf("%s (%s)", u.URL, u.Reason))
	}
	report.Add(level, "reachability", fmt.Sprintf(
		"%d of %d known page(s) (%s) could not be fetched. At or above %s the ingest is "+
			"refused rather than producing an index over part of a site. First few: %s",
		len(result.Unreachable), result.Declared(), percent(share),
		percent(domain.SiteIncompleteFatalShare), strings.Join(why, "; ")))
}

// hierarchy reports what will place the pages, and how much that is worth.
func hierarchy(result *crawl.Result, report *domain.InspectReport) {
	h := result.Hierarchy
	report.PredictedSource = h.Source.String()
	if h.Inferred {
		report.PredictedTier = domain.TierInferred
		report.Add(domain.LevelWarn, "hierarchy",
			"no navigation source placed enough of the page set to be believed, so pages "+
				"will be nested by URL path. That is inference: it keeps a command "+
				"reference together, but nothing corroborates it.")
	} else {
		report.PredictedTier = domain.TierDeclared
		report.Add(domain.LevelOK, "hierarchy", fmt.Sprintf(
			"'%s' places the page set and will supply each page's section number and "+
				"ancestry.", h.Source))
	}
	if len(h.PlacedByPath) > 0 {
		report.Add(domain.LevelWarn, "unplaced pages", fmt.Sprintf(
			"%d page(s) are named by no navigation source and will be nested by URL path "+
				"instead. They are placed, never dropped -- dropping them is how a command "+
				"reference goes missing from an index that reports success.",
			len(h.PlacedByPath)))
	}
}

// furniture reports the blocks repeated across the site, which are stripped
// as navigation before chunking.
func furniture(result *crawl.Result, report *domain.InspectReport) error {
	parsed := make([]parsedPage, 0, len(result.Pages))
	for _, url := range result.Order {
		page, ok := result.Pages[url]
		if !ok {
			continue
		}
		p, err := parsePage(page, placementOf(result, url))
		if err != nil {
			return err
		}
		parsed = append(parsed, p)
	}
	chrome := chromeTexts(parsed)
	if len(chrome) == 0 {
		return nil
	}
	report.Add(domain.LevelOK, "page furniture", fmt.Sprintf(
		"%d block(s) repeat across at least half the site and will be stripped as "+
			"navigation before chunking.", len(chrome)))
	return nil
}

// clientRendered names the pages whose content arrives only once a browser
// runs them.
//
// Named rather than refused: an empty shell that reached 'ready' is
// indistinguishable from a page that genuinely says little, and headless
// rendering is a follow-up rather than a dependency in the critical path.
func clientRendered(result *crawl.Result) ([]string, error) {
	var shells []string
	for _, url := range result.Order {
		page, ok := result.Pages[url]
		if !ok {
			continue
		}
		shell, err := isShell(page.Body)
		if err != nil {
			return nil, err
		}
		if shell {
			shells = append(shells, url)
		}
	}
	return shells, nil
}

// isShell reports a page that is mostly application rather than document.
func isShell(body []byte) (bool, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return false, fmt.Errorf("parse a page: %w", err)
	}
	scriptChars := 0
	doc.Find("script").Each(func(_ int, s *goquery.Selection) {
		scriptChars += len([]rune(s.Text()))
	})
	doc.Find("script, style").Remove()

	text := doc.Find("body")
	if text.Length() == 0 {
		text = doc.Selection
	}
	textChars := len([]rune(strings.Join(strings.Fields(text.Text()), " ")))
	if textChars > clientRenderedMaxTextChars {
		return false, nil
	}
	return float64(scriptChars) >= float64(max(1, textChars))*clientRenderedScriptRatio, nil
}

// placementOf is a page's placement, or the empty one where the hierarchy
// does not name it.
func placementOf(result *crawl.Result, url string) nav.Placement {
	return result.Hierarchy.ByURL()[url]
}

func first(values []string, n int) []string {
	return values[:min(len(values), n)]
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// percent renders a share as Python's "{:.0%}" does.
func percent(share float64) string {
	return fmt.Sprintf("%.0f%%", share*100)
}
