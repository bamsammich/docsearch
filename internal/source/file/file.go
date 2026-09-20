// Package file reads one file on disk as a document, extracted by the
// adapter its suffix selects.
//
// Ported from python/docsearch/ingest.py.
package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest"
)

// Source is one file, read through the extractor registry.
type Source struct {
	extractor ingest.Extractor
	path      string
	digest    string
}

// New reads path through extractor. The path is resolved when the source is
// acquired, so identity is absolute whatever the caller passed.
func New(path string, extractor ingest.Extractor) *Source {
	return &Source{extractor: extractor, path: path}
}

func (*Source) Kind() domain.SourceKind { return domain.SourceKindFile }

func (s *Source) Identity() string { return s.path }

func (s *Source) Digest() string { return s.digest }

// Acquire resolves the path, refuses what cannot be read, and hashes it.
//
// The format check happens before the hash so an unusable file fails without
// being read twice, and it fails the same way every time: an unsupported
// suffix is not worth a retry.
func (s *Source) Acquire(ctx context.Context, _ ingest.Progress) error {
	absolute, err := filepath.Abs(s.path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", s.path, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", absolute, err)
	}
	s.path = resolved

	info, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a file: %s", s.path)
	}
	if !s.extractor.Supports(s.path) {
		return fmt.Errorf("%w: %s", ingest.ErrUnsupportedFormat, filepath.Ext(s.path))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.digest, err = hashFile(s.path)
	return err
}

func (s *Source) Extract(ctx context.Context, _ ingest.Progress) (*domain.Extraction, error) {
	return s.extractor.Extract(ctx, s.path)
}

// hashFile is the file's SHA-256, read a megabyte at a time so a 400MB
// manual does not become 400MB of memory.
func hashFile(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = fh.Close() }()

	sum := sha256.New()
	if _, err := io.CopyBuffer(sum, fh, make([]byte, 1<<20)); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
