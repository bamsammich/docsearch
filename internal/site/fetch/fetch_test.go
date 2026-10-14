package fetch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/site/fetch/fetchtest"
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
	etag string
	// robotsRedirect is where /robots.txt redirects to. Setting it implies a
	// redirect; "none" sends one with no Location at all.
	robotsRedirect string
	// robotsStatus answers /robots.txt with that status instead of a body,
	// for the cases about what each class of answer means.
	robotsStatus int
	requests     atomic.Int64
}

func TestFetch(t *testing.T) { suite.Run(t, new(FetchSuite)) }

func (s *FetchSuite) SetupTest() {
	s.requests.Store(0)
	s.robotsBody = ""
	s.robotsStatus = 0
	s.robotsRedirect = ""
	s.etag = ""
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", s.serveRobots)
	// Where a redirected robots.txt lands, serving rules of its own so a
	// case can tell a followed redirect from an ignored one.
	mux.HandleFunc("/elsewhere/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		s.requests.Add(1)
		s.write(w, "User-agent: *\nDisallow: /private/\n")
	})
	// A chain that never arrives, for the hop limit.
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		http.Redirect(w, r, "/hop/"+r.URL.Path, http.StatusFound)
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

// serveRobots answers /robots.txt with whatever the case asked for: a chosen
// status, a body, or the 404 that means the host serves none.
func (s *FetchSuite) serveRobots(w http.ResponseWriter, _ *http.Request) {
	s.requests.Add(1)
	switch {
	case s.robotsRedirect != "" || s.robotsStatus/100 == 3:
		s.redirectRobots(w)
	case s.robotsStatus != 0:
		w.WriteHeader(s.robotsStatus)
	case s.robotsBody == "":
		http.Error(w, "not found", http.StatusNotFound)
	default:
		s.write(w, s.robotsBody)
	}
}

// redirectRobots answers with a redirect: to robotsRedirect where a case
// named one, to the moved file otherwise, and with no Location at all for
// "none".
func (s *FetchSuite) redirectRobots(w http.ResponseWriter) {
	destination := s.robotsRedirect
	if destination == "" {
		destination = "/elsewhere/robots.txt"
	}
	if destination != "none" {
		w.Header().Set("Location", destination)
	}
	status := s.robotsStatus
	if status == 0 {
		status = http.StatusMovedPermanently
	}
	w.WriteHeader(status)
}

func (s *FetchSuite) write(w http.ResponseWriter, body string) {
	_, err := w.Write([]byte(body))
	s.Require().NoError(err)
}

// fetcher returns a fetcher with its own cache, and a guard that approves
// the test server's loopback address.
func (s *FetchSuite) fetcher(opts fetch.Options) *fetch.Fetcher {
	return fetcherOn(s.cache(), opts)
}

// cache is one the case keeps a handle on, so it can plant a stored copy or
// read one back.
func (*FetchSuite) cache() *fetchtest.Memory {
	return fetchtest.New()
}

// fetcherOn builds a fetcher over a cache the case already holds.
func fetcherOn(cache fetch.Cache, opts fetch.Options) *fetch.Fetcher {
	if opts.Guard == nil {
		opts.Guard = allowLoopback
	}
	if opts.Interval == 0 {
		opts.Interval = time.Millisecond
	}
	return fetch.New(cache, opts)
}

// host is the key a stored robots.txt sits under: the server's name without
// its port.
func (s *FetchSuite) host() string {
	u, err := url.Parse(s.server.URL)
	s.Require().NoError(err)
	return u.Hostname()
}

// storedRobots plants a copy of a host's robots.txt read at a chosen time, so
// a case can decide whether the crawler reads it again.
func (s *FetchSuite) storedRobots(cache fetch.Cache, body string, age time.Duration) {
	s.Require().NoError(cache.PutRobots(s.T().Context(), s.host(), &fetch.RobotsFile{
		Body: body, FetchedAt: time.Now().UTC().Add(-age),
	}))
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

// RFC 9309 section 2.4: a cached robots.txt may decide what the crawler
// fetches for 24 hours, so a host that tightens its rules is obeyed the next
// day rather than never.
func (s *FetchSuite) TestARobotsFileOlderThanADayIsReadAgain() {
	cache := s.cache()
	s.storedRobots(cache, "User-agent: *\n", 25*time.Hour)
	s.robotsBody = "User-agent: *\nDisallow: /private/\n"

	_, err := fetcherOn(cache, fetch.Options{}).Fetch(
		s.T().Context(), s.server.URL+"/private/secret.html", true)
	s.Require().Error(err)
	s.Contains(err.Error(), "robots.txt disallows",
		"the rules the host serves now, not the ones it served yesterday")
}

func (s *FetchSuite) TestARobotsFileWithinADayIsNotReadAgain() {
	cache := s.cache()
	s.storedRobots(cache, "User-agent: *\n", time.Hour)
	// The host has changed its mind, and is not asked.
	s.robotsBody = "User-agent: *\nDisallow: /private/\n"

	before := s.requests.Load()
	got, err := fetcherOn(cache, fetch.Options{}).Fetch(
		s.T().Context(), s.server.URL+"/private/secret.html", true)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, got.Status)
	s.Equal(before+1, s.requests.Load(), "one request for the page, none for robots.txt")
}

// RFC 9309 section 2.3.1.4: a robots.txt that answers a server error is
// undefined, and an undefined robots.txt is a complete disallow. Reading it
// as permission, which is what an empty body means, would crawl a host that
// never said it could be crawled.
func (s *FetchSuite) TestAnUnreachableRobotsFileDisallowsEverything() {
	s.robotsStatus = http.StatusInternalServerError

	_, err := s.fetcher(fetch.Options{}).Fetch(
		s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().Error(err)
	s.Contains(err.Error(), "robots.txt disallows")
}

// The same section allows a copy past its age where the host cannot be
// reached, which is what keeps one server error from stopping a crawl the
// host's own rules allow.
func (s *FetchSuite) TestAnUnreachableRobotsFileKeepsTheStoredCopy() {
	cache := s.cache()
	s.storedRobots(cache, "User-agent: *\n", 25*time.Hour)
	s.robotsStatus = http.StatusInternalServerError

	got, err := fetcherOn(cache, fetch.Options{}).Fetch(
		s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, got.Status)
}

// RFC 9309 section 2.3.1.2: a crawler follows at least five consecutive
// redirects to reach a robots.txt. A host that moved the file still has one,
// and its rules still bind.
func (s *FetchSuite) TestARedirectedRobotsFileIsFollowedAndObeyed() {
	s.robotsStatus = http.StatusMovedPermanently
	f := s.fetcher(fetch.Options{})

	// The file it redirects to disallows /private/, so reaching those rules
	// is the only way the fetch is refused.
	_, err := f.Fetch(s.T().Context(), s.server.URL+"/private/secret.html", true)
	s.Require().Error(err)
	s.Contains(err.Error(), "robots.txt disallows")

	// Everything the moved file does not disallow is still fetchable, so the
	// redirect is followed rather than read as a blanket refusal.
	got, err := f.Fetch(s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, got.Status)
}

// Past the hop limit the same section permits treating the file as
// unavailable, which disallows nothing. A chain that never arrives must not
// stop the crawl, and must not loop either.
func (s *FetchSuite) TestARobotsRedirectChainThatNeverArrivesIsUnavailable() {
	s.robotsRedirect = "/hop/a"

	got, err := s.fetcher(fetch.Options{}).Fetch(
		s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, got.Status)
}

// A redirect carrying no Location is a file nobody can read. The host
// answered, so it is unavailable rather than undefined.
func (s *FetchSuite) TestARobotsRedirectWithNoLocationIsUnavailable() {
	s.robotsStatus = http.StatusFound
	s.robotsRedirect = "none"

	got, err := s.fetcher(fetch.Options{}).Fetch(
		s.T().Context(), s.server.URL+"/page.html", true)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, got.Status)
}

// A cancelled fetch says it was cancelled. Reading the cancellation as an
// unreachable host would answer with a complete disallow, so an operator who
// stopped an ingest would be told robots.txt refused them.
func (s *FetchSuite) TestACancelledFetchReportsTheCancellation() {
	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	_, err := s.fetcher(fetch.Options{}).Fetch(ctx, s.server.URL+"/page.html", true)
	s.Require().ErrorIs(err, context.Canceled)
}
