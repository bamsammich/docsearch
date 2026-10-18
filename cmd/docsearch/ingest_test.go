package main

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/document"
)

// RefreshSuite covers which source `docsearch refresh` re-crawls.
type RefreshSuite struct{ suite.Suite }

func TestRefresh(t *testing.T) { suite.Run(t, new(RefreshSuite)) }

var library = []document.Document{
	{DocID: "example-docs", Source: "https://docs.example.com/", SourceKind: domain.SourceKindSite},
	{DocID: "manual", Source: "/library/manual.pdf", SourceKind: domain.SourceKindFile},
}

// A refresh re-crawls the site's URL. The identifier is derived from it but
// is not an address, and ingesting it would look for a file of that name.
func (s *RefreshSuite) TestARefreshCrawlsTheSiteNotTheIdentifier() {
	got, err := siteOf(library, "example-docs")
	s.Require().NoError(err)
	s.Equal("https://docs.example.com/", got)
}

func (s *RefreshSuite) TestAFileHasNothingToRecrawl() {
	_, err := siteOf(library, "manual")
	s.Require().Error(err)
	s.Contains(err.Error(), "ingested from a file")
}

func (s *RefreshSuite) TestAnUnknownDocumentIsNamed() {
	_, err := siteOf(library, "nope")
	s.Require().ErrorContains(err, "no such document: nope")
}
