package html_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/suite"
	nethtml "golang.org/x/net/html"

	"github.com/bamsammich/docsearch/internal/adapter/html"
)

// maxDroppedShare is the most of a page's prose Parse may drop.
const maxDroppedShare = 0.02

// RecallSuite holds Parse to a reference that knows nothing of its block
// allowlist: every run of text that reads as prose must reach an item.
//
// Each fixture under testdata/generators is a live page from one
// documentation generator, its markup kept and every letter and digit of its
// text replaced, so lengths and layout survive while the prose does not.
type RecallSuite struct{ suite.Suite }

func TestRecall(t *testing.T) { suite.Run(t, new(RecallSuite)) }

func (s *RecallSuite) TestParseKeepsEveryGeneratorsProse() {
	paths, err := filepath.Glob(filepath.Join("testdata", "generators", "*.html"))
	s.Require().NoError(err)
	s.Require().NotEmpty(paths)
	for _, path := range paths {
		s.Run(filepath.Base(path), func() {
			raw, err := os.ReadFile(path)
			s.Require().NoError(err)
			_, items, err := html.Parse(string(raw))
			s.Require().NoError(err)
			dropped, total := droppedProse(s.T(), string(raw), items)
			s.Require().Positive(total)
			s.T().Logf("dropped %d of %d prose chars (%.1f%%)",
				dropped, total, 100*float64(dropped)/float64(total))
			s.LessOrEqual(float64(dropped), maxDroppedShare*float64(total))
		})
	}
}

// skipped is what the reference does not read: what the audit that set the
// threshold removed, text for screen readers, and the labels of controls.
// On the live pages behind the fixtures, everything aria-hidden that reads as
// prose was a notice to crawlers, an assistant panel or a search dialog.
const skipped = "script, style, nav, footer, noscript, svg, template, head, " +
	`[aria-hidden="true"], .sr-only, .visually-hidden, button, select, option`

// inline is what the reference reads through when it groups text into runs.
var inline = map[string]bool{
	"span": true, "a": true, "code": true, "em": true, "strong": true, "b": true, "i": true,
	"small": true, "sup": true, "sub": true, "kbd": true, "mark": true, "abbr": true,
	"u": true, "s": true, "label": true, "br": true, "wbr": true,
}

// droppedProse counts the characters of src's prose that no item or heading
// holds, and all of its prose. Text is grouped into runs by its nearest
// non-inline ancestor, and a run is prose when it has 40 or more characters
// and 6 or more words. A text node of a prose run counts as held when some
// item or heading contains it, whitespace aside.
func droppedProse(t *testing.T, src string, items []html.Item) (dropped, total int) {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	doc.Find(skipped).Remove()
	haystack := heldText(items)
	runs := runCollector{runs: map[*nethtml.Node][]string{}}
	for _, n := range doc.Find("body").Nodes {
		runs.walk(n)
	}
	for _, run := range runs.inOrder() {
		if isProse(strings.Join(run, " ")) {
			d, n := missing(run, haystack)
			dropped, total = dropped+d, total+n
		}
	}
	return dropped, total
}

// missing counts the characters of run's text nodes that haystack lacks,
// and all of them.
func missing(run []string, haystack string) (dropped, total int) {
	for _, text := range run {
		n := utf8.RuneCountInString(text)
		total += n
		if !strings.Contains(haystack, text) {
			dropped += n
		}
	}
	return dropped, total
}

// heldText is every item's text and heading, normalized, one per line.
func heldText(items []html.Item) string {
	held := make([]string, 0, len(items))
	for _, item := range items {
		held = append(held, normalize(item.Text))
		for _, heading := range item.HeadingPath {
			held = append(held, normalize(heading))
		}
	}
	return strings.Join(held, "\n")
}

func isProse(text string) bool {
	return utf8.RuneCountInString(text) >= 40 && len(strings.Fields(text)) >= 6
}

// runCollector groups text by container, in document order.
type runCollector struct {
	runs  map[*nethtml.Node][]string
	order []*nethtml.Node
}

func (r *runCollector) walk(n *nethtml.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != nethtml.TextNode {
			r.walk(c)
			continue
		}
		if text := normalize(c.Data); text != "" {
			r.add(container(c), text)
		}
	}
}

func (r *runCollector) add(box *nethtml.Node, text string) {
	if _, ok := r.runs[box]; !ok {
		r.order = append(r.order, box)
	}
	r.runs[box] = append(r.runs[box], text)
}

func (r *runCollector) inOrder() [][]string {
	out := make([][]string, len(r.order))
	for i, box := range r.order {
		out[i] = r.runs[box]
	}
	return out
}

// container is the nearest ancestor of n that is not inline.
func container(n *nethtml.Node) *nethtml.Node {
	box := n.Parent
	for box.Type == nethtml.ElementNode && inline[box.Data] && box.Parent != nil {
		box = box.Parent
	}
	return box
}

func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
