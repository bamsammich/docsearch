// Package adapter picks the format adapter for a source file by its suffix.
// Each adapter lives in its own subpackage, turns a file into a
// domain.Extraction, and is ported from python/docsearch/adapters: the tests
// hold every adapter to Python goldens in testdata/adapters, and
// test/integration holds them to the Python extraction of every document in
// a local library.
//
// Adding a format is one subpackage plus one entry in bySuffix. The chunker
// is untouched. PDF is not ported yet, so For refuses a .pdf.
package adapter

import (
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

// Extract reads the file at path into an extraction.
type Extract func(path string) (*domain.Extraction, error)

var bySuffix = map[string]Extract{
	".md":       markdown.Extract,
	".markdown": markdown.Extract,
	".html":     html.Extract,
	".htm":      html.Extract,
	".docx":     docx.Extract,
	".txt":      text.Extract,
	".text":     text.Extract,
}

// For returns the adapter for path's suffix, compared without case.
func For(path string) (Extract, error) {
	name := filepath.Base(path)
	suffix := pystr.Suffix(name)
	if extract, ok := bySuffix[strings.ToLower(suffix)]; ok {
		return extract, nil
	}
	if suffix == "" {
		suffix = name
	}
	return nil, fmt.Errorf("%w: no adapter for '%s'; supported: %s",
		ErrUnsupportedFormat, suffix, strings.Join(SupportedSuffixes(), ", "))
}

// IsSupported reports whether For has an adapter for path.
func IsSupported(path string) bool {
	_, ok := bySuffix[strings.ToLower(pystr.Suffix(filepath.Base(path)))]
	return ok
}

// SupportedSuffixes lists every suffix with an adapter, sorted.
func SupportedSuffixes() []string {
	return slices.Sorted(maps.Keys(bySuffix))
}
