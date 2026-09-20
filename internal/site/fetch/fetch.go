package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/jimsmart/grobotstxt"

	"github.com/bamsammich/docsearch/internal/urlguard"
)

// UserAgent identifies the crawler and where to complain about it.
const UserAgent = "docsearch/0.1 (+https://github.com/bamsammich/docsearch)"

const (
	// DefaultInterval is the wait between requests to one host. A
	// documentation site is someone else's server, and a crawl of a few
	// hundred pages has no reason to hurry.
	DefaultInterval = 500 * time.Millisecond
	// DefaultMaxRedirects is where a redirect chain becomes a loop or a
	// trap rather than navigation.
	DefaultMaxRedirects = 5
	// DefaultMaxFetches is a backstop against a seed that addresses far
	// more than a manual. The crawler applies the real budget; this is the
	// floor under a bug.
	DefaultMaxFetches = 5000
	// DefaultTimeout bounds one request.
	DefaultTimeout = 20 * time.Second
)

// ErrFetch is returned for a URL that could not be fetched, with a reason
// for the operator.
var ErrFetch = errors.New("fetch failed")

// Fetched is one response, from the network or from the cache.
type Fetched struct {
	URL         string
	FinalURL    string
	ContentType string
	Body        []byte
	Status      int
	FromCache   bool
}

// Guard decides whether a URL may be fetched, and at which addresses.
type Guard func(ctx context.Context, raw string) (*urlguard.Target, error)

// Options configure a Fetcher. The zero value of each field takes the
// default beside it.
type Options struct {
	// Guard checks each URL. A nil guard uses urlguard.Check, which refuses
	// loopback and every other address a crawler has no business reaching;
	// a test server is exactly that, so tests supply their own.
	Guard Guard
	// Resolver looks up hosts for the default guard. A nil resolver uses
	// the system's.
	Resolver urlguard.Resolver
	// Transport dials and speaks HTTP. A nil transport dials only the
	// addresses the guard approved for the request's host.
	Transport http.RoundTripper
	UserAgent string
	// Interval is the wait between requests to one host.
	Interval     time.Duration
	Timeout      time.Duration
	MaxRedirects int
	MaxFetches   int
	// IgnoreRobots fetches without asking robots.txt. Reserved for a
	// caller fetching its own site.
	IgnoreRobots bool
}

// Fetcher fetches URLs through the cache, the guard, robots.txt and a rate
// limit. It is safe for one goroutine; the crawler fetches in order.
type Fetcher struct {
	cache        Cache
	client       *http.Client
	guard        Guard
	robots       map[string]string
	lastRequest  map[string]time.Time
	userAgent    string
	interval     time.Duration
	maxRedirects int
	maxFetches   int
	fetches      int
	ignoreRobots bool
}

// New returns a Fetcher storing into cache.
func New(cache Cache, opts Options) *Fetcher {
	f := &Fetcher{
		cache:        cache,
		guard:        opts.Guard,
		robots:       map[string]string{},
		lastRequest:  map[string]time.Time{},
		userAgent:    or(opts.UserAgent, UserAgent),
		interval:     orDuration(opts.Interval, DefaultInterval),
		maxRedirects: orInt(opts.MaxRedirects, DefaultMaxRedirects),
		maxFetches:   orInt(opts.MaxFetches, DefaultMaxFetches),
		ignoreRobots: opts.IgnoreRobots,
	}
	if f.guard == nil {
		resolver := opts.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		f.guard = func(ctx context.Context, raw string) (*urlguard.Target, error) {
			return urlguard.Check(ctx, raw, resolver)
		}
	}
	transport := opts.Transport
	if transport == nil {
		transport = pinnedTransport()
	}
	f.client = &http.Client{
		Transport: transport,
		Timeout:   orDuration(opts.Timeout, DefaultTimeout),
		// Redirects are followed by hand: every hop goes through the guard.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return f
}

// Fetch retrieves one URL, serving an unchanged body from the cache.
// revalidate false returns any stored copy without asking the server, which
// is what re-chunking an already-crawled site wants.
func (f *Fetcher) Fetch(ctx context.Context, raw string, revalidate bool) (*Fetched, error) {
	target, err := Normalize(raw)
	if err != nil {
		return nil, err
	}
	cached, hit, err := f.cache.Get(ctx, target)
	if err != nil {
		return nil, err
	}
	if hit && !revalidate {
		return fromCache(cached), nil
	}
	allowed, err := f.allowedByRobots(ctx, target)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: robots.txt disallows %s", ErrFetch, target)
	}
	return f.follow(ctx, target, cached, hit)
}

// follow requests the URL and each redirect it answers with, up to the
// limit.
func (f *Fetcher) follow(
	ctx context.Context,
	target string,
	cached *Response,
	hit bool,
) (*Fetched, error) {
	current := target
	for range f.maxRedirects + 1 {
		res, err := f.hop(ctx, current, conditional(cached, hit))
		if err != nil {
			return nil, err
		}
		if !isRedirect(res.Status) {
			return f.settle(ctx, target, current, res, cached, hit)
		}
		if current, err = redirectTo(current, res.Location); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: %s: more than %d redirects", ErrFetch, target, f.maxRedirects)
}

// hop requests one URL of a redirect chain, within the budget.
func (f *Fetcher) hop(
	ctx context.Context,
	raw string,
	headers map[string]string,
) (*response, error) {
	if f.fetches >= f.maxFetches {
		return nil, fmt.Errorf("%w: fetch budget of %d exhausted", ErrFetch, f.maxFetches)
	}
	return f.request(ctx, raw, headers)
}

// settle turns the response that ended the redirect chain into a result: a
// 304 confirms the stored copy, anything else replaces it.
func (f *Fetcher) settle(ctx context.Context, target, final string, res *response,
	cached *Response, hit bool,
) (*Fetched, error) {
	if res.Status == http.StatusNotModified && hit {
		if err := f.cache.Touch(ctx, target); err != nil {
			return nil, err
		}
		return fromCache(cached), nil
	}
	return f.store(ctx, target, final, res)
}

// store caches a response and returns it.
func (f *Fetcher) store(
	ctx context.Context,
	target, final string,
	res *response,
) (*Fetched, error) {
	sum := sha256.Sum256(res.Body)
	entry := &Response{
		URL: target, FinalURL: final, Status: res.Status, ContentType: res.ContentType,
		ETag: res.ETag, LastModified: res.LastModified, Body: res.Body,
		SHA256: hex.EncodeToString(sum[:]), FetchedAt: now(),
	}
	if err := f.cache.Put(ctx, entry); err != nil {
		return nil, err
	}
	return &Fetched{
		URL: target, FinalURL: final, Status: res.Status,
		Body: res.Body, ContentType: res.ContentType,
	}, nil
}

// response is one HTTP response, read.
type response struct {
	ContentType  string
	ETag         string
	LastModified string
	Location     string
	Body         []byte
	Status       int
}

// request checks the URL with the guard, waits out the host's interval, and
// reads the response.
func (f *Fetcher) request(
	ctx context.Context,
	raw string,
	headers map[string]string,
) (*response, error) {
	checked, err := f.guard(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrFetch, raw, err)
	}
	f.wait(checked.URL.Hostname())
	f.fetches++
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrFetch, raw, err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return f.do(withAllowedAddrs(req, checked.Addrs))
}

func (f *Fetcher) do(req *http.Request) (*response, error) {
	res, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrFetch, req.URL, err)
	}
	defer res.Body.Close()
	out := &response{
		Status:       res.StatusCode,
		ContentType:  res.Header.Get("Content-Type"),
		ETag:         res.Header.Get("ETag"),
		LastModified: res.Header.Get("Last-Modified"),
		Location:     res.Header.Get("Location"),
	}
	if isRedirect(out.Status) || out.Status == http.StatusNotModified {
		// Neither carries a body worth reading.
		return out, nil
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrFetch, req.URL, err)
	}
	out.Body = body
	return out, nil
}

// wait keeps one request at a time to a host, spaced by the interval.
func (f *Fetcher) wait(host string) {
	if last, ok := f.lastRequest[host]; ok {
		if remaining := f.interval - time.Since(last); remaining > 0 {
			time.Sleep(remaining)
		}
	}
	f.lastRequest[host] = time.Now()
}

// allowedByRobots reports whether the host's robots.txt permits the URL.
func (f *Fetcher) allowedByRobots(ctx context.Context, raw string) (bool, error) {
	if f.ignoreRobots {
		return true, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false, fmt.Errorf("%w: %s: %w", ErrFetch, raw, err)
	}
	body, err := f.robotsFor(ctx, u)
	if err != nil {
		return false, err
	}
	return grobotstxt.AgentAllowed(body, f.userAgent, raw), nil
}

// robotsFor reads a host's robots.txt, from memory, then the cache, then
// the host. A host that will not serve one has disallowed nothing: absent
// is permission, per the standard.
func (f *Fetcher) robotsFor(ctx context.Context, u *url.URL) (string, error) {
	host := u.Hostname()
	if body, ok := f.robots[host]; ok {
		return body, nil
	}
	body, ok, err := f.cache.Robots(ctx, host)
	if err != nil {
		return "", err
	}
	if !ok {
		// Not routed through Fetch: asking robots.txt whether robots.txt may
		// be fetched does not terminate.
		body = f.fetchRobots(ctx, u.Scheme+"://"+u.Host+"/robots.txt")
		if err := f.cache.PutRobots(ctx, host, body); err != nil {
			return "", err
		}
	}
	f.robots[host] = body
	return body, nil
}

// fetchRobots reads a host's robots.txt, or "" when it does not serve one.
func (f *Fetcher) fetchRobots(ctx context.Context, raw string) string {
	res, err := f.request(ctx, raw, nil)
	if err != nil || res.Status != http.StatusOK {
		return ""
	}
	return string(res.Body)
}

// conditional asks the server for the body only if it changed, which is
// what makes refreshing a large site cheap.
func conditional(cached *Response, hit bool) map[string]string {
	headers := map[string]string{}
	if !hit {
		return headers
	}
	if cached.ETag != "" {
		headers["If-None-Match"] = cached.ETag
	}
	if cached.LastModified != "" {
		headers["If-Modified-Since"] = cached.LastModified
	}
	return headers
}

// redirectTo is the normalized target of a redirect.
func redirectTo(from, location string) (string, error) {
	if location == "" {
		return "", fmt.Errorf("%w: %s: redirect without a location", ErrFetch, from)
	}
	base, err := url.Parse(from)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrFetch, from, err)
	}
	next, err := base.Parse(location)
	if err != nil {
		return "", fmt.Errorf("%w: %s: unresolvable redirect to %s", ErrFetch, from, location)
	}
	return Normalize(next.String())
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

func fromCache(r *Response) *Fetched {
	return &Fetched{
		URL: r.URL, FinalURL: r.FinalURL, Status: r.Status,
		Body: r.Body, ContentType: r.ContentType, FromCache: true,
	}
}

// allowedAddrsKey carries the addresses the guard approved for a request to
// the dialler, so the connection cannot go anywhere else.
type allowedAddrsKey struct{}

func withAllowedAddrs(req *http.Request, addrs []netip.Addr) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), allowedAddrsKey{}, addrs))
}

// pinnedTransport dials only the addresses the guard approved for this
// request. The name was validated before the request; a resolver is free to
// answer differently the second time, and dialling the approved address
// closes that window rather than narrowing it. TLS still presents the name
// from the URL, so certificates verify as usual.
func pinnedTransport() http.RoundTripper {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		addrs, ok := ctx.Value(allowedAddrsKey{}).([]netip.Addr)
		if !ok || len(addrs) == 0 {
			return nil, fmt.Errorf("%w: no address was approved for %s", ErrFetch, addr)
		}
		return dialApproved(ctx, dialer, network, addr, addrs)
	}
	return transport
}

// dialApproved tries each approved address in turn, at the port the
// request named.
func dialApproved(ctx context.Context, dialer *net.Dialer, network, addr string,
	addrs []netip.Addr,
) (net.Conn, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrFetch, addr, err)
	}
	var last error
	for _, a := range addrs {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		last = dialErr
	}
	return nil, fmt.Errorf("%w: %s: %w", ErrFetch, addr, last)
}

// or and its siblings take the default when a field was left zero.
func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func orInt(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

func orDuration(v, fallback time.Duration) time.Duration {
	if v == 0 {
		return fallback
	}
	return v
}
