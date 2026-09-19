// Package html reads an HTML document, taking its structure from h1 to h6.
// Ported from python/docsearch/adapters/html.py. A local .html file and a
// page of a crawled site are the same parsing problem, so the site crawler
// calls Parse too and supplies its own locators.
//
// The Python reference parses with lxml through BeautifulSoup; this package
// parses with goquery over the HTML5 algorithm. The two agree on the
// documents the tests hold; they differ on markup a browser would repair,
// such as content after </body>, which lxml drops and HTML5 moves into the
// body.
package html

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	nethtml "golang.org/x/net/html"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
)

// CSS selectors for the elements the walk reads.
const (
	// chromeSelector matches what is removed before reading. Chrome is not
	// content, and a navigation repeated on every page of a site is the
	// single largest source of duplicated term mass.
	chromeSelector  = "script, style, nav, footer"
	blockSelector   = "p, li, pre, blockquote, td, dd, dt"
	headingSelector = "h1, h2, h3, h4, h5, h6"
)

// Item is one block of an HTML document, with the heading ancestry above it.
type Item struct {
	// Fragment is the id of the nearest heading at or above the item,
	// without the '#'; nil when that heading has none.
	Fragment    *string
	Text        string
	HeadingPath []string
}

// Parse returns the title and items of one HTML document.
//
// Prose is each block's text nodes, stripped and joined with a space. A
// <pre> is read verbatim instead, since generated documentation wraps every
// token in its own <span> and joining those would put a code sample on one
// line.
func Parse(src string) (string, []Item, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(src))
	if err != nil {
		return "", nil, fmt.Errorf("parse html: %w", err)
	}
	doc.Find(chromeSelector).Remove()

	body := doc.Find("body").First()
	if body.Length() == 0 {
		body = doc.Selection
	}
	w := walker{codes: takeCode(body), items: []Item{}}
	body.Find(headingSelector + ", " + blockSelector).
		Each(func(_ int, el *goquery.Selection) { w.visit(el) })

	title := ""
	if t := doc.Find("title").First(); t.Length() > 0 {
		if s, ok := onlyString(t.Nodes[0]); ok {
			title = pystr.Strip(s)
		}
	}
	return title, w.items, nil
}

// Extract reads the local HTML file at path.
func Extract(path string) (*domain.Extraction, error) {
	raw, err := pystr.ReadFile(path)
	if err != nil {
		return nil, err
	}
	title, items, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	blocks := make([]domain.Block, 0, len(items))
	offset := 0
	for _, item := range items {
		blocks = append(blocks, domain.NewOffsetBlock(item.HeadingPath, offset, item.Text))
		offset += utf8.RuneCountInString(item.Text) + 1
	}
	if title == "" {
		title = pystr.Stem(filepath.Base(path))
	}
	return domain.NewExtraction(title, "html", "h1_h6_nesting", blocks), nil
}

// walker visits headings and block tags in document order.
type walker struct {
	fragment *string
	codes    []string
	stack    domain.HeadingStack
	items    []Item
}

func (w *walker) visit(el *goquery.Selection) {
	if el.Is("pre") {
		// Emitted whatever it is nested in: code is the content a reader
		// came for, and flattening it into a parent loses its lines.
		w.emitCode()
		return
	}
	text := proseText(el.Nodes[0])
	switch {
	case text == "":
	case el.Is(headingSelector):
		level := int(goquery.NodeName(el)[1] - '0') // "h3" is level 3
		w.stack.Push(level, text)
		// An anchor is how a citation lands on the section rather than the
		// top of the page, so it is tracked from the heading that owns it.
		w.fragment = headingAnchor(el)
	case el.ParentsFiltered(blockSelector).Length() == 0:
		// A nested block tag's text is already in its parent's.
		w.items = append(w.items, w.item(text))
	}
}

func (w *walker) emitCode() {
	if len(w.codes) == 0 {
		return
	}
	text := w.codes[0]
	w.codes = w.codes[1:]
	if text != "" {
		w.items = append(w.items, w.item(text))
	}
}

func (w *walker) item(text string) Item {
	return Item{Fragment: w.fragment, Text: text, HeadingPath: w.stack.Path()}
}

// takeCode reads out the text of every outermost <pre> under root, in
// document order, and empties each. Emptying rather than removing keeps the
// element in place for the walk, which pairs each <pre> it meets with the
// next text here, and keeps a list item or cell that holds code from also
// contributing a flattened copy of it. Every outermost <pre> is chosen
// before any is emptied, since emptying detaches the <pre> nested inside.
func takeCode(root *goquery.Selection) []string {
	outermost := root.Find("pre").FilterFunction(func(_ int, pre *goquery.Selection) bool {
		return pre.ParentsFiltered("pre").Length() == 0
	})
	codes := make([]string, 0, outermost.Length())
	outermost.Each(func(_ int, pre *goquery.Selection) {
		codes = append(codes, codeText(pre.Text()))
		pre.Empty()
	})
	return codes
}

// codeText is the verbatim text of a <pre>, less the blank lines framing it.
func codeText(text string) string {
	lines := pystr.SplitLines(text)
	for len(lines) > 0 && pystr.Strip(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && pystr.Strip(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// headingAnchor is the heading's own id, else the id of the first element
// inside it that has one, stripped; nil when that is empty.
func headingAnchor(h *goquery.Selection) *string {
	id, _ := h.Attr("id")
	if pystr.Strip(id) == "" {
		id, _ = h.Find("[id]").First().Attr("id")
	}
	id = pystr.Strip(id)
	if id == "" {
		return nil
	}
	return &id
}

// proseText is BeautifulSoup's get_text(" ", strip=True): every text node
// under n, each stripped, the empty ones dropped, joined with a space.
// goquery's Text concatenates the nodes unstripped, which would keep the
// markup's line breaks and indentation inside prose.
func proseText(n *nethtml.Node) string {
	var parts []string
	eachText(n, func(s string) {
		if s = pystr.Strip(s); s != "" {
			parts = append(parts, s)
		}
	})
	return strings.Join(parts, " ")
}

// eachText calls f with every text node under n in document order. Comments
// are not text.
func eachText(n *nethtml.Node, f func(string)) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == nethtml.TextNode {
			f(c.Data)
		}
		eachText(c, f)
	}
}

// onlyString is BeautifulSoup's Tag.string: the text of n's one child, or
// of that child's one child, and so on; false when a node on the way has
// more or fewer than one child.
func onlyString(n *nethtml.Node) (string, bool) {
	c := n.FirstChild
	if c == nil || c.NextSibling != nil {
		return "", false
	}
	switch c.Type {
	case nethtml.TextNode, nethtml.CommentNode:
		return c.Data, true
	case nethtml.ElementNode:
		return onlyString(c)
	default:
		return "", false
	}
}
