// Package fetchtest holds a crawl cache that keeps nothing.
//
// A crawl suite is about what the crawler asks for and what it does with the
// answers, never about what survives a restart. Giving one a real cache costs
// it a database container and tests the wrong thing; internal/site/fetch/pgcache
// has the suite that covers storage.
package fetchtest

import (
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/bamsammich/docsearch/internal/site/fetch"
)

// Memory is a fetch.Cache in a map, for a test that needs a crawl to have
// somewhere to put a response.
//
// Guarded by a mutex because a crawl fetches concurrently, so the race
// detector would find an unguarded map the first time a suite crawled more
// than one page.
type Memory struct {
	responses map[string]fetch.Response
	robots    map[string]fetch.RobotsFile
	mu        sync.Mutex
}

// New is an empty cache.
func New() *Memory {
	return &Memory{
		responses: map[string]fetch.Response{},
		robots:    map[string]fetch.RobotsFile{},
	}
}

func (m *Memory) Get(_ context.Context, url string) (*fetch.Response, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.responses[url]
	if !ok {
		return nil, false, nil
	}
	// A copy, so a caller holding the result cannot reach into the cache
	// through the body it was handed.
	stored.Body = append([]byte(nil), stored.Body...)
	return &stored, true, nil
}

func (m *Memory) Put(_ context.Context, r *fetch.Response) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := *r
	stored.Body = append([]byte(nil), r.Body...)
	m.responses[r.URL] = stored
	return nil
}

// Touch records that a conditional request confirmed the stored copy. The
// time it would set is read by nothing here, so a hit on a URL nobody stored
// is not an error.
func (*Memory) Touch(_ context.Context, _ string) error { return nil }

func (m *Memory) Robots(_ context.Context, host string) (*fetch.RobotsFile, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.robots[host]
	if !ok {
		return nil, false, nil
	}
	return &stored, true, nil
}

func (m *Memory) PutRobots(_ context.Context, host string, r *fetch.RobotsFile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.robots[host] = *r
	return nil
}

// URLs is every URL the cache holds, for a suite asserting what a crawl
// fetched rather than what it returned.
func (m *Memory) URLs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Sorted(maps.Keys(m.responses))
}
