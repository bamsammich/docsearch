package fetch

import (
	"context"
	"time"
)

// Response is one stored HTTP response.
type Response struct {
	FetchedAt    time.Time
	URL          string
	FinalURL     string
	ContentType  string
	ETag         string
	LastModified string
	SHA256       string
	Body         []byte
	Status       int
}

// RobotsFile is a host's robots.txt and when the host last served it.
//
// The age is what decides whether the copy may still say what the crawler
// fetches: RFC 9309 section 2.4 allows a cached copy for at most 24 hours.
type RobotsFile struct {
	FetchedAt time.Time
	Body      string
}

// Cache holds the raw responses a crawl fetched, apart from the search
// index: the index is read on every search and shipped for offline use,
// while this is worker-only and holds bytes that would bloat it.
//
// Keeping them buys four things: crawl and ingest separate, so re-chunking a
// site makes no requests; conditional GET on refresh; a cancelled crawl
// resumes; and what was actually fetched stays inspectable.
type Cache interface {
	// Get is the stored response for url, and whether one was stored.
	Get(ctx context.Context, url string) (*Response, bool, error)
	// Put stores a response, replacing any stored under the same URL.
	Put(ctx context.Context, r *Response) error
	// Touch records that a conditional request confirmed the stored copy.
	Touch(ctx context.Context, url string) error
	// Robots is the stored robots.txt for a host, and whether one was
	// stored. An empty body means the host serves none, which is permission.
	Robots(ctx context.Context, host string) (*RobotsFile, bool, error)
	// PutRobots stores a host's robots.txt.
	PutRobots(ctx context.Context, host string, r *RobotsFile) error
}

// now is when a response or a robots.txt was read from the host.
func now() time.Time {
	return time.Now().UTC()
}
