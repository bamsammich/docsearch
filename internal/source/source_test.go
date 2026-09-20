package source_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest/mocks"
	"github.com/bamsammich/docsearch/internal/source"
	"github.com/bamsammich/docsearch/internal/source/site"
)

// SourceSuite covers which kind of ingest a target names, and which targets
// are refused before anything is read.
type SourceSuite struct {
	suite.Suite
	registry *source.Registry
	root     string
}

func TestSource(t *testing.T) { suite.Run(t, new(SourceSuite)) }

func (s *SourceSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.registry = source.New(
		mocks.NewMockExtractor(s.T()),
		[]string{s.root},
		filepath.Join(s.T().TempDir(), "cache.db"),
		site.Options{},
	)
}

func (s *SourceSuite) TestAURLBecomesASiteIngest() {
	got, err := s.registry.For("https://example.com/docs/", true)
	s.Require().NoError(err)
	s.Equal(domain.SourceKindSite, got.Kind())
	s.Equal("https://example.com/docs/", got.Identity())
}

func (s *SourceSuite) TestAPathInsideTheRootBecomesAFileIngest() {
	path := filepath.Join(s.root, "guide.md")
	s.Require().NoError(os.WriteFile(path, []byte("# Guide\n"), 0o600))

	got, err := s.registry.For(path, true)
	s.Require().NoError(err)
	s.Equal(domain.SourceKindFile, got.Kind())
}

func (s *SourceSuite) TestAPathOutsideEveryRootIsRefused() {
	// A caller cannot name a path the operator never offered.
	_, err := s.registry.For("/etc/passwd", true)
	s.Require().Error(err)
	s.Contains(err.Error(), "/etc/passwd")
}

func (s *SourceSuite) TestASiteNeedsSomewhereToCache() {
	registry := source.New(
		mocks.NewMockExtractor(s.T()), []string{s.root}, "", site.Options{})

	_, err := registry.For("https://example.com/docs/", true)
	s.Require().ErrorContains(err, "needs a fetch cache")
}

func (s *SourceSuite) TestWhatCountsAsAURL() {
	tests := []struct {
		target string
		note   string
		want   bool
	}{
		{target: "https://example.com/docs/", want: true},
		{target: "http://example.com/docs/", want: true},
		{target: "HTTPS://Example.com/docs/", want: true, note: "a scheme is case-insensitive"},
		{target: "/library/guide.md", want: false},
		{target: "guide.md", want: false},
		{
			target: "file:///library/guide.md",
			want:   false,
			note:   "a file URL is a path spelled oddly, not a site",
		},
		{
			target: "C:\\library\\guide.md",
			want:   false,
			note:   "a drive letter is not a scheme worth crawling",
		},
	}
	for _, tt := range tests {
		s.Run(tt.target, func() {
			s.Equal(tt.want, source.IsURL(tt.target), tt.note)
		})
	}
}
