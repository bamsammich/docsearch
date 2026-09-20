package fetch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/urlguard"
)

// FetchSuite exercises the fetcher against a real HTTP server on an
// ephemeral port, as the Python tests do.
type FetchSuite struct {
	suite.Suite
	server *httptest.Server
	// robotsBody is served at /robots.txt; "" answers 404.
	robotsBody string
	// etag is served with /page.html and revalidated against.
	etag     string
	requests atomic.Int64
}

func TestFetch(t *testing.T) { suite.Run(t, new(FetchSuite)) }

func (s *FetchSuite) SetupTest() {
	s.requests.Store(0)
	s.robotsBody = ""
	s.etag = ""
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		s.requests.Add(1)
		if s.robotsBody == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		s.write(w, s.robotsBody)
	})
	mux.HandleFunc("/page.html", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		if s.etag != "" {
			w.Header().Set("ETag", s.etag)
			if r.Header.Get("If-None-Match") == s.etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		s.write(w, "<html><body><p>a page</p></body></html>")
	})
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		http.Redirect(w, r, "/page.html", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/private/secret.html", func(w http.ResponseWriter, _ *http.Request) {
		s.requests.Add(1)
		s.write(w, "secret")
	})
	s.server = httptest.NewServer(mux)
	s.T().Cleanup(s.server.Close)
}

func (s *FetchSuite) write(w http.ResponseWriter, body string) {
	_, err := w.Write([]byte(body))
	s.Require().NoError(err)
}

// fetcher returns a fetcher with its own cache, and a guard that approves
// the test server's loopback address.
func (s *FetchSuite) fetcher(opts fetch.Options) *fetch.Fetcher {
	cache, err := fetch.OpenSQLiteCache(s.T().Context(), filepath.Join(s.T().TempDir(), "cache.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(cache.Close()) })
	if opts.Guard == nil {
		opts.Guard = allowLoopback
	}
	if opts.Interval == 0 {
		opts.Interval = time.Millisecond
	}
	return fetch.New(cache, opts)
}

// allowLoopback approves every URL, at the address it names.
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

func (s *FetchSuite) TestFetchesAndCachesAPage() {
	f := s.fetcher(fetch.Options{})
	got, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, got.Status)
	s.Contains(string(got.Body), "a page")
	s.False(got.FromCache)

	// A second fetch without revalidation asks the server nothing.
	before := s.requests.Load()
	again, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", false)
	s.Require().NoError(err)
	s.True(again.FromCache)
	s.Equal(before, s.requests.Load())
}

func (s *FetchSuite) TestAnUnchangedPageTransfersNoBody() {
	s.etag = `"v1"`
	f := s.fetcher(fetch.Options{})
	_, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().NoError(err)
	again, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().NoError(err)
	s.True(again.FromCache, "a 304 serves the stored copy")
	s.Contains(string(again.Body), "a page")
}

func (s *FetchSuite) TestRedirectsAreFollowedAndTheFinalURLRecorded() {
	f := s.fetcher(fetch.Options{})
	got, err := f.Fetch(s.T().Context(), s.server.URL+"/moved", true)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, got.Status)
	s.Equal(s.server.URL+"/moved", got.URL)
	s.Equal(s.server.URL+"/page.html", got.FinalURL)
}

func (s *FetchSuite) TestARedirectLoopIsRefused() {
	f := s.fetcher(fetch.Options{})
	_, err := f.Fetch(s.T().Context(), s.server.URL+"/loop", true)
	s.Require().ErrorIs(err, fetch.ErrFetch)
	s.Contains(err.Error(), "redirects")
}

func (s *FetchSuite) TestEveryRedirectHopIsGuarded() {
	// Validating only the seed leaves the guard trivially bypassable.
	var seen []string
	f := s.fetcher(
		fetch.Options{Guard: func(ctx context.Context, raw string) (*urlguard.Target, error) {
			seen = append(seen, raw)
			if strings.HasSuffix(raw, "/page.html") {
				return nil, urlguard.ErrBlocked
			}
			return allowLoopback(ctx, raw)
		}},
	)
	_, err := f.Fetch(s.T().Context(), s.server.URL+"/moved", true)
	s.Require().ErrorIs(err, urlguard.ErrBlocked)
	s.Len(seen, 3, "robots.txt, the seed, and the hop it redirected to")
	s.Contains(seen[0], "/robots.txt", "even robots.txt is checked")
}

func (s *FetchSuite) TestA404IsReturnedRatherThanRaised() {
	// The crawler decides what a missing page means; the fetcher reports it.
	f := s.fetcher(fetch.Options{})
	got, err := f.Fetch(s.T().Context(), s.server.URL+"/missing.html", true)
	s.Require().NoError(err)
	s.Equal(http.StatusNotFound, got.Status)
}

func (s *FetchSuite) TestRobotsDisallowIsObeyed() {
	s.robotsBody = "User-agent: *\nDisallow: /private/\n"
	f := s.fetcher(fetch.Options{})
	_, err := f.Fetch(s.T().Context(), s.server.URL+"/private/secret.html", true)
	s.Require().ErrorIs(err, fetch.ErrFetch)
	s.Contains(err.Error(), "robots.txt disallows")

	allowed, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, allowed.Status)
}

func (s *FetchSuite) TestAMissingRobotsPermitsEverything() {
	// Absent is permission, per the standard.
	f := s.fetcher(fetch.Options{})
	got, err := f.Fetch(s.T().Context(), s.server.URL+"/private/secret.html", true)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, got.Status)
}

func (s *FetchSuite) TestRobotsIsFetchedOncePerHost() {
	s.robotsBody = "User-agent: *\nAllow: /\n"
	f := s.fetcher(fetch.Options{})
	for range 3 {
		_, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
		s.Require().NoError(err)
	}
	s.Equal(int64(4), s.requests.Load(), "one robots.txt and three pages")
}

func (s *FetchSuite) TestRobotsCanBeOverriddenForASiteTheOperatorRuns() {
	s.robotsBody = "User-agent: *\nDisallow: /\n"
	f := s.fetcher(fetch.Options{IgnoreRobots: true})
	got, err := f.Fetch(s.T().Context(), s.server.URL+"/private/secret.html", true)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, got.Status)
}

func (s *FetchSuite) TestABlockedURLNeverReachesTheNetwork() {
	blocked := errors.New("refused by the guard")
	f := s.fetcher(fetch.Options{
		IgnoreRobots: true,
		Guard: func(context.Context, string) (*urlguard.Target, error) {
			return nil, blocked
		},
	})
	_, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().ErrorIs(err, blocked)
	s.Zero(s.requests.Load())
}

func (s *FetchSuite) TestTheFetchBudgetIsAFloorUnderABug() {
	f := s.fetcher(fetch.Options{IgnoreRobots: true, MaxFetches: 2})
	for range 2 {
		_, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
		s.Require().NoError(err)
	}
	_, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().ErrorIs(err, fetch.ErrFetch)
	s.Contains(err.Error(), "budget")
}

func (s *FetchSuite) TestRequestsToOneHostAreSpaced() {
	f := s.fetcher(fetch.Options{IgnoreRobots: true, Interval: 80 * time.Millisecond})
	start := time.Now()
	for range 3 {
		_, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
		s.Require().NoError(err)
	}
	s.GreaterOrEqual(time.Since(start), 160*time.Millisecond, "two waits between three requests")
}

func (s *FetchSuite) TestTheConnectionGoesToTheAddressTheGuardApproved() {
	// The guard approves an address nothing is listening on, so a fetch
	// that reached the server would mean the dialler ignored it.
	f := s.fetcher(fetch.Options{
		IgnoreRobots: true,
		Timeout:      2 * time.Second,
		Guard: func(_ context.Context, raw string) (*urlguard.Target, error) {
			u, err := url.Parse(raw)
			if err != nil {
				return nil, err
			}
			return &urlguard.Target{
				URL:   u,
				Addrs: []netip.Addr{netip.MustParseAddr("127.0.0.9")},
			}, nil
		},
	})
	_, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().ErrorIs(err, fetch.ErrFetch)
	s.Zero(s.requests.Load())
}
