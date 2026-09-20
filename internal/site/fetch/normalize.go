// Package fetch retrieves URLs with the manners and the guards a crawler
// owes a stranger's site: one request at a time per host, robots.txt obeyed,
// every hop checked by the URL guard, and every response cached.
//
// Ported from python/docsearch/fetch.py and fetchcache.py. Nothing else in
// the ingest path talks to the network.
package fetch

import (
	"fmt"
	"net/url"
	"strings"
)

// Normalize gives a resource one spelling, so the frontier and the cache
// agree. It lowercases the scheme and host, drops the default port, the
// fragment and any credentials, and resolves dot segments.
//
// The trailing slash, the query and its ordering are left alone: each can
// change which resource is addressed. On at least one real generator
// "/docs/" is the page and "/docs" is a 404.
func Normalize(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("unparseable URL %s: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	path := removeDotSegments(u.EscapedPath())
	if path == "" {
		path = "/"
	}
	// Assembled rather than rendered through url.URL, which would escape a
	// path that is already escaped.
	out := scheme + "://" + authority(u, scheme) + path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out, nil
}

// authority is the host, lowercased and bracketed where it is an address
// literal, with the port unless it is the scheme's default.
func authority(u *url.URL, scheme string) string {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if port == "" || (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		return host
	}
	return host + ":" + port
}

// removeDotSegments is RFC 3986 section 5.2.4, written out rather than left
// to path.Clean, which strips the trailing slash.
func removeDotSegments(path string) string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		out = applySegment(out, seg)
	}
	joined := strings.Join(out, "/")
	// Splitting drops no information, so a path that ended in "." or ".."
	// regains the slash those segments stood in for.
	if endsInDots(path) && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	return joined
}

// applySegment adds one path segment: "." adds nothing, and ".." drops the
// segment before it, but never the leading empty one that is the root.
func applySegment(out []string, seg string) []string {
	switch seg {
	case ".":
		return out
	case "..":
		if len(out) > 1 {
			return out[:len(out)-1]
		}
		return out
	}
	return append(out, seg)
}

func endsInDots(path string) bool {
	return strings.HasSuffix(path, "/.") || strings.HasSuffix(path, "/..")
}
