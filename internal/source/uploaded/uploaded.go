// Package uploaded reads a file someone uploaded, from the database it was
// stored in.
//
// Adapters read a path, so the bytes are written to a private temporary
// directory under the file's own name, which is what picks the adapter, and
// the directory is removed once the extraction is done.
package uploaded

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/upload"
)

// Source is one uploaded file.
type Source struct {
	store     upload.Store
	extractor ingest.Extractor
	key       string
	file      upload.File
}

// New reads the file under key from store.
func New(key string, store upload.Store, extractor ingest.Extractor) *Source {
	return &Source{key: key, store: store, extractor: extractor}
}

func (*Source) Kind() domain.SourceKind { return domain.SourceKindUpload }

func (s *Source) Identity() string { return upload.Target(s.key) }

func (s *Source) Digest() string { return s.file.SHA256 }

// Acquire reads the stored file and refuses one no adapter reads. The digest
// is taken again from the bytes rather than trusted from the row.
func (s *Source) Acquire(ctx context.Context, _ ingest.Progress) error {
	file, err := s.store.Get(ctx, s.key)
	if err != nil {
		return err
	}
	if !s.extractor.Supports(file.Filename) {
		return fmt.Errorf("%w: %s", ingest.ErrUnsupportedFormat, filepath.Ext(file.Filename))
	}
	file.SHA256 = upload.Digest(file.Content)
	s.file = file
	return nil
}

// Extract writes the bytes where an adapter can read them, and removes them
// after.
func (s *Source) Extract(ctx context.Context, _ ingest.Progress) (*domain.Extraction, error) {
	dir, err := os.MkdirTemp("", "docsearch-upload-")
	if err != nil {
		return nil, fmt.Errorf("stage upload %s: %w", s.key, err)
	}
	path := filepath.Join(dir, filepath.Base(s.file.Filename))
	extraction, err := s.extractFrom(ctx, path)
	return extraction, errors.Join(err, os.RemoveAll(dir))
}

func (s *Source) extractFrom(ctx context.Context, path string) (*domain.Extraction, error) {
	if err := os.WriteFile(path, s.file.Content, 0o600); err != nil {
		return nil, fmt.Errorf("stage upload %s: %w", s.key, err)
	}
	return s.extractor.Extract(ctx, path)
}
