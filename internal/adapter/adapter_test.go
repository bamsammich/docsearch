package adapter_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/domain"
)

// fixtureDir holds synthetic documents and the Python adapters' extraction of
// each, written by scripts/adapter_goldens.py.
const fixtureDir = "../../testdata/adapters"

// AdapterSuite holds every adapter, through the registry, to the Python
// adapter it was ported from.
type AdapterSuite struct {
	suite.Suite
	pdf      *pdf.Extractor
	registry *adapter.Registry
}

func (s *AdapterSuite) SetupSuite() {
	ex, err := pdf.New()
	s.Require().NoError(err)
	s.pdf, s.registry = ex, adapter.New(ex)
}

func (s *AdapterSuite) TearDownSuite() {
	s.Require().NoError(s.pdf.Close())
}

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

			got, err := s.registry.Extract(context.Background(), path)
			s.Require().NoError(err)
			if strings.HasSuffix(e.Name(), ".pdf") {
				// PDFium lays out a page's display text differently from
				// MuPDF, a contents row on one line where MuPDF breaks it at
				// every cell. Nothing parses that text, so it is compared
				// with whitespace collapsed; everything else is exact.
				want.Pages, got.Pages = collapsed(want.Pages), collapsed(got.Pages)
			}
			s.Equal(&want, jsonDiagnostics(s.T(), got))
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
		{path: "manual.PDF", want: true},
		{path: "archive.tar.gz", want: false},
		{path: ".md", want: false},
		{path: "Makefile", want: false},
	}
	for _, tt := range tests {
		s.Run(tt.path, func() {
			s.Equal(tt.want, adapter.IsSupported(tt.path))
		})
	}
}

func (s *AdapterSuite) TestForNamesWhatItCannotRead() {
	_, err := s.registry.Extract(context.Background(), "/library/archive.tar.gz")
	s.Require().ErrorIs(err, adapter.ErrUnsupportedFormat)
	s.Contains(err.Error(), "no adapter for '.gz'")

	_, err = s.registry.Extract(context.Background(), "/library/Makefile")
	s.Require().ErrorIs(err, adapter.ErrUnsupportedFormat)
	s.Contains(err.Error(), "no adapter for 'Makefile'; supported: .docx, .htm")
}

// jsonDiagnostics returns ext with its diagnostics as JSON decodes them, so
// they compare with a decoded golden: Go builds ints and string slices where
// decoding gives float64 and []any.
func jsonDiagnostics(t *testing.T, ext *domain.Extraction) *domain.Extraction {
	t.Helper()
	raw, err := json.Marshal(ext.Diagnostics)
	if err != nil {
		t.Fatalf("diagnostics do not marshal: %v", err)
	}
	out := *ext
	out.Diagnostics = nil
	if err := json.Unmarshal(raw, &out.Diagnostics); err != nil {
		t.Fatalf("diagnostics do not unmarshal: %v", err)
	}
	return &out
}

// collapsed is page text with every run of whitespace read as one space.
func collapsed(pages map[int]string) map[int]string {
	out := make(map[int]string, len(pages))
	for n, text := range pages {
		out[n] = strings.Join(strings.Fields(text), " ")
	}
	return out
}
