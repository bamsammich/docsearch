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

func (s *HTMLSuite) TestParseReadsMintlifyParagraphSpans() {
	raw, err := os.ReadFile(filepath.Join("testdata", "mintlify.html"))
	s.Require().NoError(err)
	_, items, err := html.Parse(string(raw))
	s.Require().NoError(err)

	type read struct{ fragment, text string }
	got := make([]read, len(items))
	for i, item := range items {
		s.Require().NotNil(item.Fragment, item.Text)
		got[i] = read{*item.Fragment, item.Text}
	}
	// The page's chrome sits outside every block, so none of it is read.
	s.Equal([]read{
		{"page-title", "Follow this guide to publish a working site."},
		{"before-you-begin", "Every page has a source file in your repository ."},
		{"before-you-begin", "Install the CLI with npm i -g example first."},
		{"before-you-begin", "A terminal"},
		{"before-you-begin", "Node.js 20 or later"},
		{"deploy", "Run the deploy command from the project root."},
		{"deploy", "example deploy\nexample status"},
		{"deploy", "Deploy without a Git provider"},
		{"deploy", "You can connect a repository later."},
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
