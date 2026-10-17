package uploaded_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/ingest/mocks"
	"github.com/bamsammich/docsearch/internal/source/uploaded"
	"github.com/bamsammich/docsearch/internal/upload"
	uploadmocks "github.com/bamsammich/docsearch/internal/upload/mocks"
)

const content = "# Manual\n\nBody.\n"

type UploadedSuite struct {
	suite.Suite
	store     *uploadmocks.MockStore
	extractor *mocks.MockExtractor
}

func TestUploaded(t *testing.T) { suite.Run(t, new(UploadedSuite)) }

func (s *UploadedSuite) SetupTest() {
	s.store = uploadmocks.NewMockStore(s.T())
	s.extractor = mocks.NewMockExtractor(s.T())
}

func (s *UploadedSuite) stored(filename string) {
	s.store.EXPECT().Get(mock.Anything, "key-1").Return(upload.File{
		Key: "key-1", Filename: filename, SHA256: "a stale digest", Content: []byte(content),
	}, nil)
}

// The digest comes from the bytes, not from the row: a re-ingest is decided
// by it, and a row is only a claim about the bytes.
func (s *UploadedSuite) TestAcquireReadsTheStoredFile() {
	s.stored("manual.md")
	s.extractor.EXPECT().Supports("manual.md").Return(true)

	source := uploaded.New("key-1", s.store, s.extractor)
	s.Require().NoError(source.Acquire(s.T().Context(), nil))
	s.Equal(domain.SourceKindUpload, source.Kind())
	s.Equal("upload:key-1", source.Identity())
	s.Equal(upload.Digest([]byte(content)), source.Digest())
}

func (s *UploadedSuite) TestAnUnreadableFormatIsRefused() {
	s.stored("photo.heic")
	s.extractor.EXPECT().Supports("photo.heic").Return(false)
	err := uploaded.New("key-1", s.store, s.extractor).Acquire(s.T().Context(), nil)
	s.Require().ErrorIs(err, ingest.ErrUnsupportedFormat)
}

// An adapter reads a path named after the file, so its suffix picks the
// adapter, and nothing of the file is left on disk afterwards.
func (s *UploadedSuite) TestExtractStagesTheFileUnderItsOwnName() {
	s.stored("manual.md")
	s.extractor.EXPECT().Supports("manual.md").Return(true)
	var staged string
	s.extractor.EXPECT().Extract(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, path string) (*domain.Extraction, error) {
			staged = path
			read, err := os.ReadFile(path)
			s.Require().NoError(err)
			s.Equal(content, string(read))
			return &domain.Extraction{}, nil
		})

	source := uploaded.New("key-1", s.store, s.extractor)
	s.Require().NoError(source.Acquire(s.T().Context(), nil))
	_, err := source.Extract(s.T().Context(), nil)
	s.Require().NoError(err)

	s.Equal("manual.md", filepath.Base(staged))
	_, err = os.Stat(filepath.Dir(staged))
	s.True(os.IsNotExist(err), "the staging directory is removed")
}
