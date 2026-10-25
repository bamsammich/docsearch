// Package html reads an HTML document, taking its structure from h1 to h6.
// A local .html file and a page of a crawled site are the same parsing
// problem, so the site crawler calls Parse too and supplies its own
// locators.
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

// CSS selectors for what is removed before the walk.
const (
	// chromeSelector matches what is never content. A navigation repeated on
	// every page of a site is the single largest source of duplicated term
	// mass.
	chromeSelector = "script, style, noscript, template, svg, nav, footer"
	// unseenSelector matches what a reader never sees as prose: text kept for
	// screen readers, notices addressed to crawlers, and the labels of
	// controls. A button inside a paragraph stays, since a site that gives a
	// term a tooltip wraps the term itself in the button.
	//
	// The hidden attribute is not here. Generators mark inactive tab panels
	// hidden, and Next.js streams a page's content into a hidden <div> that a
	// script later moves into place.
	unseenSelector = `[aria-hidden="true"], .sr-only, .visually-hidden, button, select, option`
	keptControl    = `p button, [data-as="p"] button`
)

// blockTags are the elements read as one block each. Mintlify writes a
// paragraph as <span data-as="p">, which is a block too. A <pre> is read
// apart from these, and so is a heading.
var blockTags = map[string]bool{
	"p": true, "li": true, "blockquote": true, "td": true, "dd": true, "dt": true,
}

// inlineTags are the elements whose text joins the text around them. Any
// other element ends a run of text and starts a new one.
var inlineTags = map[string]bool{
	"a": true, "abbr": true, "b": true, "bdi": true, "bdo": true, "big": true, "br": true,
	"cite": true, "code": true, "data": true, "del": true, "dfn": true, "em": true,
	"font": true, "i": true, "img": true, "ins": true, "kbd": true, "label": true,
	"mark": true, "q": true, "rp": true, "rt": true, "ruby": true, "s": true, "samp": true,
	"small": true, "span": true, "strike": true, "strong": true, "sub": true, "sup": true,
	"time": true, "tt": true, "u": true, "var": true, "wbr": true,
}

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
// Every text node outside chromeSelector and unseenSelector reaches an item.
// A block tag is one item, and so is each run of text that no block tag
// holds, bounded by the nearest elements that are not inline. Generators put
// prose in callouts, cards, captions and bare <div>s, so a list of the tags
// that hold prose would drop whatever the next generator chose.
//
// Prose is the text nodes, stripped and joined with a space. A <pre> is read
// verbatim instead, since generated documentation wraps every token in its
// own <span> and joining those would put a code sample on one line. So is a
// multi-line <code> that no block holds, which is how some sites write a
// sample without a <pre>.
func Parse(src string) (string, []Item, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(src))
	if err != nil {
		return "", nil, fmt.Errorf("parse html: %w", err)
	}
	doc.Find(chromeSelector).Remove()
	doc.Find(unseenSelector).Not(keptControl).Remove()

	body := doc.Find("body").First()
	if body.Length() == 0 {
		body = doc.Selection
	}
	w := walker{codes: takeCode(body), items: []Item{}}
	for _, n := range body.Nodes {
		w.walk(n, false)
	}
	w.flush()

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
	return domain.NewExtraction(title, "html", domain.SourceHTMLNesting, blocks), nil
}

// walker reads headings, blocks and runs of text in document order.
type walker struct {
	fragment *string
	codes    []string
	stack    domain.HeadingStack
	items    []Item
	// run is the text read since the last element that ends a run.
	run []string
}

// walk reads the children of n. Under a heading or a block, read is true:
// that element's text is already read, so only the headings, blocks and
// <pre>s inside it remain.
func (w *walker) walk(n *nethtml.Node, read bool) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case nethtml.TextNode:
			if s := proseString(c.Data); s != "" && !read {
				w.run = append(w.run, s)
			}
		case nethtml.ElementNode:
			w.visit(c, read)
		default:
		}
	}
}

// visit reads one element and what is inside it.
func (w *walker) visit(n *nethtml.Node, read bool) {
	switch {
	case n.Data == "pre":
		// Emitted whatever it is nested in: code is the content a reader
		// came for, and flattening it into a parent loses its lines.
		w.flush()
		w.emitCode()
	case headingLevel(n) > 0:
		w.flush()
		w.heading(n)
		w.walk(n, true)
	case isBlock(n):
		w.flush()
		if !read {
			w.emit(ProseText(n))
		}
		// A nested block's text is already in this one's.
		w.walk(n, true)
	case !read && isCode(n):
		w.flush()
		w.emit(codeText(allText(n)))
	case inlineTags[n.Data]:
		w.walk(n, read)
	default:
		w.flush()
		w.walk(n, read)
		w.flush()
	}
}

func (w *walker) heading(h *nethtml.Node) {
	text := ProseText(h)
	if text == "" {
		return
	}
	w.stack.Push(headingLevel(h), text)
	// An anchor is how a citation lands on the section rather than the top
	// of the page, so it is tracked from the heading that owns it.
	w.fragment = headingAnchor(goquery.NewDocumentFromNode(h).Selection)
}

// flush emits the run read so far.
func (w *walker) flush() {
	if len(w.run) > 0 {
		w.emit(strings.Join(w.run, " "))
		w.run = nil
	}
}

func (w *walker) emit(text string) {
	if text != "" {
		w.items = append(w.items, w.item(text))
	}
}

func (w *walker) emitCode() {
	if len(w.codes) == 0 {
		return
	}
	text := w.codes[0]
	w.codes = w.codes[1:]
	w.emit(text)
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

// headingLevel is 3 for an <h3>, and 0 for an element that is no heading.
func headingLevel(n *nethtml.Node) int {
	if len(n.Data) == 2 && n.Data[0] == 'h' && n.Data[1] >= '1' && n.Data[1] <= '6' {
		return int(n.Data[1] - '0')
	}
	return 0
}

// isBlock reports whether n is read as one block.
func isBlock(n *nethtml.Node) bool {
	if blockTags[n.Data] {
		return true
	}
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == "data-as" && a.Val == "p" {
			return true
		}
	}
	return false
}

// isCode reports whether n is a <code> whose text spans lines.
func isCode(n *nethtml.Node) bool {
	return n.Data == "code" && strings.Contains(pystr.Strip(allText(n)), "\n")
}

// allText is every text node under n, concatenated as written.
func allText(n *nethtml.Node) string {
	var b strings.Builder
	eachText(n, func(s string) { b.WriteString(s) })
	return b.String()
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

// zeroWidth holds the characters that take no space on the page. Python's
// str.strip keeps them, so a text node holding only these survives the
// strip. Mintlify puts a zero-width space inside the anchor link of every
// heading, and read as text it would start every heading path.
const zeroWidth = "\u200b\u200c\u200d\ufeff"

// proseString is the data of one text node, stripped; "" when nothing but
// zeroWidth characters remain. A run and ProseText both read text nodes
// with it, so the two agree on which nodes hold text.
func proseString(s string) string {
	if s = pystr.Strip(s); strings.Trim(s, zeroWidth) == "" {
		return ""
	}
	return s
}

// ProseText is BeautifulSoup's get_text(" ", strip=True): every text node
// under n, each read with proseString, the empty ones dropped, joined with
// a space. goquery's Text concatenates the nodes unstripped, which would
// keep the markup's line breaks and indentation inside prose. The site
// packages read navigation labels with it, so they read as the adapters do.
func ProseText(n *nethtml.Node) string {
	var parts []string
	eachText(n, func(s string) {
		if s = proseString(s); s != "" {
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
