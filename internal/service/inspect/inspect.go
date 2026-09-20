// Package inspect answers what ingest would make of a document, without
// writing anything.
//
// Whether a document will be searchable is decided by what structure can be
// derived from it, and that is knowable before any of it is stored. A
// document that will fail, or will succeed badly, says so here rather than
// after it has cost a worker an hour.
//
// Ported from python/docsearch/inspect.py.
package inspect

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/site"
	"github.com/bamsammich/docsearch/internal/site/crawl"
)

// Reader reads a PDF's primitives without extracting it. The port is
// declared here because this package is what calls it.
type Reader interface {
	Read(ctx context.Context, path string) (*pdf.Document, error)
}

// Formats says whether an adapter reads a path.
type Formats interface {
	Supports(path string) bool
}

// Crawler walks a site. Reconnaissance crawls for real: every question worth
// asking about a site is a question about what the server returns.
type Crawler interface {
	Crawl(ctx context.Context, seed string) (*crawl.Result, error)
}

// Service inspects a target.
type Service struct {
	reader  Reader
	formats Formats
	crawler Crawler
}

// New builds the service. crawler may be nil where a deployment inspects
// files only, and a URL is then refused rather than silently skipped.
func New(reader Reader, formats Formats, crawler Crawler) *Service {
	return &Service{reader: reader, formats: formats, crawler: crawler}
}

// Inspect reports what could be derived from a target, writing nothing.
func (s *Service) Inspect(ctx context.Context, target string) (*domain.InspectReport, error) {
	if isURL(target) {
		return s.site(ctx, target)
	}
	return s.file(ctx, target)
}

// file reports what a document on disk offers.
func (s *Service) file(ctx context.Context, path string) (*domain.InspectReport, error) {
	if !s.formats.Supports(path) {
		report := &domain.InspectReport{Target: path, Format: "unsupported"}
		report.Add(domain.LevelBlocked, "format", fmt.Sprintf(
			"%v: %s", ingest.ErrUnsupportedFormat, filepath.Ext(path)))
		return report, nil
	}

	report := &domain.InspectReport{
		Target:          path,
		Format:          strings.TrimPrefix(pystr.Suffix(filepath.Base(path)), "."),
		PredictedSource: "unknown",
	}
	if !strings.EqualFold(filepath.Ext(path), ".pdf") {
		// Every other format declares its headings in the markup, so there
		// is nothing to reconstruct and nothing to predict.
		report.PredictedSource = "markup"
		report.PredictedTier = domain.TierAuthoritative
		report.Add(domain.LevelOK, "structure",
			"headings are declared by the markup itself, so nothing about the structure "+
				"is inferred and there is nothing to reconstruct.")
		return report, nil
	}

	doc, err := s.reader.Read(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	pdf.Inspect(doc, report)
	return report, nil
}

// site crawls a seed and reports what ingest would make of it.
func (s *Service) site(ctx context.Context, seed string) (*domain.InspectReport, error) {
	report := &domain.InspectReport{
		Target:          seed,
		Format:          "site",
		PredictedSource: "unknown",
	}
	if s.crawler == nil {
		report.Add(domain.LevelBlocked, "address",
			"this deployment inspects files only, so a site cannot be crawled here.")
		return report, nil
	}

	result, err := s.crawler.Crawl(ctx, seed)
	if err != nil {
		// A refused address is a finding rather than a failure: the caller
		// asked a question and the answer is that the URL is not permitted.
		report.Add(domain.LevelBlocked, "address", err.Error())
		return report, nil //nolint:nilerr // the refusal is the report
	}
	if err := site.Inspect(result, report); err != nil {
		return nil, err
	}
	return report, nil
}

// isURL reports whether a target names a site rather than a path on disk.
func isURL(target string) bool {
	return strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://")
}
