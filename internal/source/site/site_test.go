package site_test

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/source/site"
	"github.com/bamsammich/docsearch/internal/urlguard"
)

// SiteSuite covers what a site source refuses before a single request goes
// out. What a crawl produces is covered by internal/site and by the parity
// specs under test/integration.
type SiteSuite struct{ suite.Suite }

func TestSite(t *testing.T) { suite.Run(t, new(SiteSuite)) }

func (s *SiteSuite) source(seed string, opts site.Options) *site.Source {
	return site.New(seed, s.cache(), opts)
}

// cache is a crawl cache in a file, which is all a source needs: whose
// library it belongs to is decided by whoever opened it.
func (s *SiteSuite) cache() *fetch.SQLiteCache {
	cache, err := fetch.OpenSQLiteCache(s.T().Context(),
		filepath.Join(s.T().TempDir(), "cache.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(cache.Close()) })
	return cache
}

func (s *SiteSuite) TestTheSeedIsCheckedAgainstTheGuardBeforeAnythingIsFetched() {
	// The fetcher guards every URL too. A job row is not proof that anything
	// validated the seed, and this runs in the worker.
	refused := errors.New("address not permitted")
	source := s.source("https://internal.example/docs/", site.Options{
		Guard: func(context.Context, string) (*urlguard.Target, error) { return nil, refused },
	})

	err := source.Acquire(s.T().Context(), nil)
	s.Require().ErrorIs(err, refused)
	s.Contains(err.Error(), "https://internal.example/docs/")
}

func (s *SiteSuite) TestIdentityIsTheNormalizedSeed() {
	// Replacement is keyed on identity, so two spellings of one seed must not
	// become two documents.
	// The default port, the case of the host and the fragment all go; the
	// query stays, because a generator that paginates on one serves
	// different pages from one path.
	source := s.source("HTTPS://Example.COM:443/docs/?utm_source=x#top", site.Options{})
	s.Equal("https://example.com/docs/?utm_source=x", source.Identity())
	s.Equal(domain.SourceKindSite, source.Kind())
}

func (s *SiteSuite) TestAnUnparseableSeedIsItsOwnIdentity() {
	source := s.source("::not a url::", site.Options{})
	s.Equal("::not a url::", source.Identity())
}

func (s *SiteSuite) TestExtractBeforeAcquireSaysSo() {
	source := s.source("https://example.com/docs/", site.Options{})
	_, err := source.Extract(s.T().Context(), nil)
	s.Require().ErrorContains(err, "extract before acquire")
}

// allowLoopback is the guard a fixture server needs. The real one refuses
// loopback, which is exactly what a fixture server is.
func allowLoopback(_ context.Context, raw string) (*urlguard.Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil {
		return nil, err
	}
	return &urlguard.Target{URL: u, Addrs: []netip.Addr{addr}}, nil
}

func (s *SiteSuite) TestASiteThatAnswersNothingIsRefused() {
	// Every page unreachable is not an empty document: it is a crawl that
	// failed, and the notes say why.
	source := s.source("http://127.0.0.1:1/docs/", site.Options{Guard: allowLoopback})
	s.Require().NoError(source.Acquire(s.T().Context(), nil))

	_, err := source.Extract(s.T().Context(), nil)
	s.Require().ErrorContains(err, "no page of this site could be fetched")
}
