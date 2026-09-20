// Package site reads a documentation site as one document, by crawling it.
//
// Ported from python/docsearch/ingest.py.
package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/site"
	"github.com/bamsammich/docsearch/internal/site/crawl"
	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/urlguard"
)

// Options bound one site ingest.
type Options struct {
	// Guard checks each URL. A nil guard takes urlguard.Check, which refuses
	// loopback and everything else a crawler has no business reaching.
	Guard fetch.Guard
	// Interval is the wait between requests to one host. Zero takes the
	// fetcher's default, which is what a stranger's site gets; an operator
	// crawling their own may reasonably lower it.
	Interval  time.Duration
	MaxPages  int
	LinkDepth int
	// Revalidate false re-chunks an already-crawled site without a single
	// request.
	Revalidate bool
	// IgnoreRobots fetches without asking robots.txt. Reserved for an
	// operator crawling their own site.
	IgnoreRobots bool
}

// Source is one site, crawled through a fetch cache.
type Source struct {
	result    *crawl.Result
	cachePath string
	seed      string
	digest    string
	opts      Options
}

// New crawls seed, caching responses at cachePath.
func New(seed, cachePath string, opts Options) *Source {
	return &Source{seed: seed, cachePath: cachePath, opts: opts}
}

func (*Source) Kind() domain.SourceKind { return domain.SourceKindSite }

func (s *Source) Identity() string {
	normalized, err := fetch.Normalize(s.seed)
	if err != nil {
		return s.seed
	}
	return normalized
}

func (s *Source) Digest() string { return s.digest }

// Acquire crawls the site and hashes what came back.
//
// The seed is checked against the guard here as well as inside the fetcher.
// A job row is not proof that anything validated it, and this is the worker.
// The crawler reports no progress of its own yet, so a caller watching a
// site ingest sees nothing until extraction. Step 6d gives the crawler a
// progress hook, because the worker is what surfaces one.
func (s *Source) Acquire(ctx context.Context, _ ingest.Progress) error {
	guard := s.opts.Guard
	if guard == nil {
		guard = func(ctx context.Context, raw string) (*urlguard.Target, error) {
			return urlguard.Check(ctx, raw, nil)
		}
	}
	if _, err := guard(ctx, s.seed); err != nil {
		return fmt.Errorf("refused %s: %w", s.seed, err)
	}

	cache, err := fetch.OpenSQLiteCache(ctx, s.cachePath)
	if err != nil {
		return fmt.Errorf("open the fetch cache: %w", err)
	}
	defer func() { _ = cache.Close() }()

	fetcher := fetch.New(cache, fetch.Options{
		Guard:        s.opts.Guard,
		Interval:     s.opts.Interval,
		IgnoreRobots: s.opts.IgnoreRobots,
	})
	s.result, err = crawl.Crawl(ctx, fetcher, s.seed, crawl.Options{
		MaxPages:   s.opts.MaxPages,
		LinkDepth:  s.opts.LinkDepth,
		Revalidate: s.opts.Revalidate,
	})
	if err != nil {
		return err
	}
	s.digest = digestOf(s.result)
	return nil
}

// Extract turns the crawl into one document.
func (s *Source) Extract(_ context.Context, _ ingest.Progress) (*domain.Extraction, error) {
	if s.result == nil {
		return nil, fmt.Errorf("%s: extract before acquire", s.seed)
	}
	if len(s.result.Pages) == 0 {
		return nil, &ingest.StructureError{Message: fmt.Sprintf(
			"%s: no page of this site could be fetched. %s",
			s.seed, strings.Join(s.result.Notes, "; "))}
	}
	return site.BuildExtraction(s.result, "")
}

// digestOf hashes every page's address and body, in address order, so the
// same site crawled twice hashes alike whatever order the frontier reached
// its pages in.
func digestOf(result *crawl.Result) string {
	sum := sha256.New()
	for _, url := range slices.Sorted(maps.Keys(result.Pages)) {
		sum.Write([]byte(url))
		body := sha256.Sum256(result.Pages[url].Body)
		sum.Write(body[:])
	}
	return hex.EncodeToString(sum.Sum(nil))
}
