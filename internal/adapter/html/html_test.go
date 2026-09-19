package html_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

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

func (s *HTMLSuite) TestExtractReportsAMissingFile() {
	_, err := html.Extract(filepath.Join(s.T().TempDir(), "missing.html"))
	s.Require().ErrorIs(err, os.ErrNotExist)
}
