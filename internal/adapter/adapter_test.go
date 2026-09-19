package adapter_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/domain"
)

// fixtureDir holds synthetic documents and the Python adapters' extraction of
// each, written by scripts/adapter_goldens.py.
const fixtureDir = "../../testdata/adapters"

// AdapterSuite holds every adapter, through the registry, to the Python
// adapter it was ported from.
type AdapterSuite struct{ suite.Suite }

func TestAdapter(t *testing.T) { suite.Run(t, new(AdapterSuite)) }

func (s *AdapterSuite) TestMatchesPythonGoldens() {
	entries, err := os.ReadDir(fixtureDir)
	s.Require().NoError(err)
	fixtures := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".golden.json") {
			continue
		}
		fixtures++
		s.Run(e.Name(), func() {
			path := filepath.Join(fixtureDir, e.Name())
			raw, err := os.ReadFile(path + ".golden.json")
			s.Require().NoError(err)
			var want domain.Extraction
			s.Require().NoError(json.Unmarshal(raw, &want))

			extract, err := adapter.For(path)
			s.Require().NoError(err)
			got, err := extract(path)
			s.Require().NoError(err)
			s.Equal(&want, got)
		})
	}
	s.Positive(fixtures)
}

func (s *AdapterSuite) TestForMatchesSuffixesWithoutCase() {
	tests := []struct {
		path string
		want bool
	}{
		{path: "guide.md", want: true},
		{path: "GUIDE.MD", want: true},
		{path: "notes.Markdown", want: true},
		{path: "page.htm", want: true},
		{path: "report.docx", want: true},
		{path: "readme.text", want: true},
		{path: "archive.tar.gz", want: false},
		{path: ".md", want: false},
		{path: "Makefile", want: false},
	}
	for _, tt := range tests {
		s.Run(tt.path, func() {
			s.Equal(tt.want, adapter.IsSupported(tt.path))
			_, err := adapter.For(tt.path)
			s.Equal(tt.want, err == nil)
		})
	}
}

func (s *AdapterSuite) TestForNamesWhatItCannotRead() {
	_, err := adapter.For("/library/archive.tar.gz")
	s.Require().ErrorIs(err, adapter.ErrUnsupportedFormat)
	s.Contains(err.Error(), "no adapter for '.gz'")

	_, err = adapter.For("/library/Makefile")
	s.Require().ErrorIs(err, adapter.ErrUnsupportedFormat)
	s.Contains(err.Error(), "no adapter for 'Makefile'; supported: .docx, .htm")
}
