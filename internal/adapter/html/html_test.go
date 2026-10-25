package html_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/suite"
	nethtml "golang.org/x/net/html"

	"github.com/bamsammich/docsearch/internal/adapter/html"
)

// HTMLSuite checks what the goldens cannot: a local file's extraction drops
// fragments, and the site crawler reads them from Parse.
type HTMLSuite struct{ suite.Suite }

func TestHTML(t *testing.T) { suite.Run(t, new(HTMLSuite)) }

func (s *HTMLSuite) TestParseTracksTheNearestHeadingAnchor() {
	// Expected values from the Python adapter's parse().
	top, networking, setup := "top", "networking", "setup"
	tests := []struct {
		fixture string
		want    []*string
	}{
		{
			fixture: "edges.html",
			want:    []*string{&top, &top, &top, &top, &top, nil, nil, nil, nil, nil, nil},
		},
		{
			fixture: "page.html",
			want: []*string{
				&networking, &setup, &setup, &setup, &setup,
				&setup, &setup, &setup, &setup, &setup, nil,
			},
		},
	}
	for _, tt := range tests {
		s.Run(tt.fixture, func() {
			raw, err := os.ReadFile(filepath.Join("../../../testdata/adapters", tt.fixture))
			s.Require().NoError(err)
			_, items, err := html.Parse(string(raw))
			s.Require().NoError(err)
			got := make([]*string, len(items))
			for i, item := range items {
				got[i] = item.Fragment
			}
			s.Equal(tt.want, got)
		})
	}
}

func (s *HTMLSuite) TestParseDoesNotLetANestedPreTakeTheNextOnesPlace() {
	_, items, err := html.Parse(
		"<body><pre>outer<pre>inner</pre>tail</pre><pre>next</pre><p>After.</p></body>")
	s.Require().NoError(err)
	texts := make([]string, len(items))
	for i, item := range items {
		texts[i] = item.Text
	}
	s.Equal([]string{"outerinnertail", "next", "After."}, texts)
}

func (s *HTMLSuite) TestParseReadsTextNoBlockHolds() {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "a run ends at an element that is not inline",
			body: `<div>Before <em>the</em> list<ul><li>Item</li></ul>after it.</div><div>Next.</div>`,
			want: []string{"Before the list", "Item", "after it.", "Next."},
		},
		{
			name: "a callout and a caption are runs",
			body: `<div class="alert" role="note"><strong>Note:</strong> read this.</div>` +
				`<figure><figcaption>Listing 1: a program</figcaption></figure>`,
			want: []string{"Note: read this.", "Listing 1: a program"},
		},
		{
			name: "a multi-line code outside a block is code",
			body: `<div><code class="hljs">curl \` + "\n" + `  -H "Accept: */*"</code></div><p>Run <code>ls</code>.</p>`,
			want: []string{"curl \\\n  -H \"Accept: */*\"", "Run ls ."},
		},
		{
			name: "a button is read only inside a paragraph",
			body: `<button>Copy</button><p>Store it in a <button>repository</button>.</p>`,
			want: []string{"Store it in a repository ."},
		},
		{
			name: "text for screen readers and crawlers is not read",
			body: `<div aria-hidden="true">For AI agents: read llms.txt.</div>` +
				`<h2>Setup<span class="sr-only">Section titled Setup</span></h2><p>Body.</p>`,
			want: []string{"Body."},
		},
		{
			name: "a hidden tab panel is read",
			body: `<div role="tabpanel">Python</div><div role="tabpanel" hidden>Go</div>`,
			want: []string{"Python", "Go"},
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			_, items, err := html.Parse("<body>" + tt.body + "</body>")
			s.Require().NoError(err)
			texts := make([]string, len(items))
			for i, item := range items {
				texts[i] = item.Text
			}
			s.Equal(tt.want, texts)
		})
	}
}

func (s *HTMLSuite) TestParseReadsMintlifyParagraphSpans() {
	raw, err := os.ReadFile(filepath.Join("testdata", "mintlify.html"))
	s.Require().NoError(err)
	_, items, err := html.Parse(string(raw))
	s.Require().NoError(err)

	type read struct{ fragment, text string }
	got := make([]read, len(items))
	for i, item := range items {
		got[i].text = item.Text
		if item.Fragment != nil {
			got[i].fragment = *item.Fragment
		}
	}
	// The notice to crawlers, the skip link and the copy button are never
	// read. The rest of the chrome is, and a site's chrome pass strips what
	// repeats on every page.
	s.Equal([]read{
		{"", "Get started"},
		{"page-title", "Follow this guide to publish a working site."},
		{"before-you-begin", "Every page has a source file in your repository ."},
		{"before-you-begin", "Install the CLI with npm i -g example first."},
		{"before-you-begin", "A terminal"},
		{"before-you-begin", "Node.js 20 or later"},
		{"deploy", "Run the deploy command from the project root."},
		{"deploy", "Terminal"},
		{"deploy", "example deploy\nexample status"},
		{"deploy", "Deploy without a Git provider"},
		{"deploy", "You can connect a repository later."},
		{"deploy", "Was this page helpful?"},
		{"deploy", "Responses are generated using AI and may contain mistakes."},
	}, got)
}

func (s *HTMLSuite) TestParseLeavesMintlifyAnchorsOutOfHeadingPaths() {
	raw, err := os.ReadFile(filepath.Join("testdata", "mintlify.html"))
	s.Require().NoError(err)
	_, items, err := html.Parse(string(raw))
	s.Require().NoError(err)

	paths := make(map[string][]string)
	for _, item := range items {
		s.Require().NotNil(item.Fragment, item.Text)
		paths[*item.Fragment] = item.HeadingPath
	}
	// Each heading's anchor link holds only a zero-width space.
	s.Equal(map[string][]string{
		"page-title":       {"Quickstart"},
		"before-you-begin": {"Quickstart", "Before you begin"},
		"deploy":           {"Quickstart", "Deploy"},
	}, paths)
}

func (s *HTMLSuite) TestProseTextDropsNodesOfOnlyZeroWidthCharacters() {
	doc, err := nethtml.Parse(strings.NewReader(
		"<p>\u200b<b>Zero\u200bwidth</b> \u200c\u200d <i>\ufeff</i>kept</p>"))
	s.Require().NoError(err)
	p := goquery.NewDocumentFromNode(doc).Find("p").Nodes[0]
	// A zero-width character inside a word is the word's, so it stays.
	s.Equal("Zero\u200bwidth kept", html.ProseText(p))
}

func (s *HTMLSuite) TestExtractReportsAMissingFile() {
	_, err := html.Extract(filepath.Join(s.T().TempDir(), "missing.html"))
	s.Require().ErrorIs(err, os.ErrNotExist)
}
