// Package adapter picks the format adapter for a source file by its suffix.
// Each adapter lives in its own subpackage, turns a file into a
// domain.Extraction, and is ported from python/docsearch/adapters: the tests
// hold every adapter to Python goldens in testdata/adapters, and
// test/integration holds them to the Python extraction of every document in
// a local library.
//
// Adding a format is one subpackage plus one entry in formats. The chunker
// is untouched.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bamsammich/docsearch/internal/adapter/docx"
	"github.com/bamsammich/docsearch/internal/adapter/html"
	"github.com/bamsammich/docsearch/internal/adapter/markdown"
	"github.com/bamsammich/docsearch/internal/adapter/text"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
)

// ErrUnsupportedFormat is returned for a file whose suffix no adapter reads.
var ErrUnsupportedFormat = errors.New("unsupported format")

// format names an adapter.
type format int

const (
	formatPDF format = iota + 1
	formatMarkdown
	formatHTML
	formatDocx
	formatText
)

var formats = map[string]format{
	".pdf":      formatPDF,
	".md":       formatMarkdown,
	".markdown": formatMarkdown,
	".html":     formatHTML,
	".htm":      formatHTML,
	".docx":     formatDocx,
	".txt":      formatText,
	".text":     formatText,
}

// PDFExtractor reads a PDF. *pdf.Extractor is one; it holds a PDF engine
// that is costly to start, so the caller starts it once and passes it in.
type PDFExtractor interface {
	Extract(ctx context.Context, path string) (*domain.Extraction, error)
}

// Registry extracts a file with the adapter its suffix names.
type Registry struct {
	pdf PDFExtractor
}

// New returns a registry that reads PDFs with pdf.
func New(pdf PDFExtractor) *Registry {
	return &Registry{pdf: pdf}
}

// Extract reads the file at path with the adapter for its suffix, compared
// without case.
func (r *Registry) Extract(ctx context.Context, path string) (*domain.Extraction, error) {
	f, err := formatOf(path)
	if err != nil {
		return nil, err
	}
	switch f {
	case formatPDF:
		return r.pdf.Extract(ctx, path)
	case formatMarkdown:
		return markdown.Extract(path)
	case formatHTML:
		return html.Extract(path)
	case formatDocx:
		return docx.Extract(path)
	case formatText:
		return text.Extract(path)
	}
	return nil, fmt.Errorf("%w: no adapter for format %d", ErrUnsupportedFormat, f)
}

// IsSupported reports whether an adapter reads path.
func IsSupported(path string) bool {
	_, err := formatOf(path)
	return err == nil
}

// SupportedSuffixes lists every suffix with an adapter, sorted.
func SupportedSuffixes() []string {
	return slices.Sorted(maps.Keys(formats))
}

func formatOf(path string) (format, error) {
	name := filepath.Base(path)
	suffix := pystr.Suffix(name)
	if f, ok := formats[strings.ToLower(suffix)]; ok {
		return f, nil
	}
	if suffix == "" {
		suffix = name
	}
	return 0, fmt.Errorf("%w: no adapter for '%s'; supported: %s",
		ErrUnsupportedFormat, suffix, strings.Join(SupportedSuffixes(), ", "))
}
