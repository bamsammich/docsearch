package file_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/ingest/mocks"
	"github.com/bamsammich/docsearch/internal/source/file"
)

// FileSuite covers what a file source refuses and what it hashes.
type FileSuite struct {
	suite.Suite
	extractor *mocks.MockExtractor
	dir       string
}

func TestFile(t *testing.T) { suite.Run(t, new(FileSuite)) }

func (s *FileSuite) SetupTest() {
	s.dir = s.T().TempDir()
	s.extractor = mocks.NewMockExtractor(s.T())
}

// write puts a file in the temporary directory and returns the path a
// source will settle on. macOS hands out temporary directories under /var,
// which is a symlink, and the source resolves symlinks as Python's
// Path.resolve does.
func (s *FileSuite) write(name, content string) string {
	s.Require().NoError(os.WriteFile(filepath.Join(s.dir, name), []byte(content), 0o600))
	resolved, err := filepath.EvalSymlinks(filepath.Join(s.dir, name))
	s.Require().NoError(err)
	return resolved
}

func (s *FileSuite) TestTheDigestIsTheFilesBytes() {
	path := s.write("guide.md", "# Operator Guide\n\nUnpack the archive.\n")
	s.extractor.EXPECT().Supports(mock.Anything).Return(true)

	source := file.New(path, s.extractor)
	s.Require().NoError(source.Acquire(s.T().Context(), nil))

	sum := sha256.Sum256([]byte("# Operator Guide\n\nUnpack the archive.\n"))
	s.Equal(hex.EncodeToString(sum[:]), source.Digest())
	s.Equal(domain.SourceKindFile, source.Kind())
}

func (s *FileSuite) TestARelativePathBecomesTheAbsoluteIdentity() {
	// Identity is what replacement is keyed on, so two spellings of one file
	// must not become two documents.
	path := s.write("guide.md", "# Guide\n")
	s.extractor.EXPECT().Supports(mock.Anything).Return(true)
	s.T().Chdir(s.dir)

	source := file.New("guide.md", s.extractor)
	s.Require().NoError(source.Acquire(s.T().Context(), nil))
	s.Equal(path, source.Identity())
}

func (s *FileSuite) TestAnUnreadableFormatIsRefusedBeforeHashing() {
	// A 400MB file with no adapter should not be read to find that out, and
	// the refusal is the same every time, so a worker must not retry it.
	path := s.write("archive.zip", "not a document")
	s.extractor.EXPECT().Supports(path).Return(false)

	source := file.New(path, s.extractor)
	err := source.Acquire(s.T().Context(), nil)

	s.Require().ErrorIs(err, ingest.ErrUnsupportedFormat)
	s.Contains(err.Error(), ".zip")
	s.Empty(source.Digest())
}

func (s *FileSuite) TestADirectoryIsNotAFile() {
	source := file.New(s.dir, s.extractor)
	s.Require().ErrorContains(source.Acquire(s.T().Context(), nil), "not a file")
}

func (s *FileSuite) TestAMissingFileSaysWhichOne() {
	missing := filepath.Join(s.dir, "absent.md")
	source := file.New(missing, s.extractor)
	s.Require().ErrorContains(source.Acquire(s.T().Context(), nil), "absent.md")
}

func (s *FileSuite) TestExtractionGoesThroughTheRegistry() {
	path := s.write("guide.md", "# Guide\n")
	want := domain.NewExtraction("Guide", "markdown", domain.SourceATXHeadings, nil)
	s.extractor.EXPECT().Supports(mock.Anything).Return(true)
	s.extractor.EXPECT().Extract(mock.Anything, mock.Anything).Return(want, nil)

	source := file.New(path, s.extractor)
	s.Require().NoError(source.Acquire(s.T().Context(), nil))
	got, err := source.Extract(s.T().Context(), nil)
	s.Require().NoError(err)
	s.Equal(want, got)
}
