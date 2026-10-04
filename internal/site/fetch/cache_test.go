package fetch_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/site/fetch"
)

// stamp is when the stored responses in these cases were read from a host.
var stamp = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)

// CacheSuite checks the response cache in a file, the shape that lives until
// step 4g of docs/plans/postgres-multiuser.md removes it.
type CacheSuite struct{ suite.Suite }

func TestCache(t *testing.T) { suite.Run(t, new(CacheSuite)) }

func (s *CacheSuite) open(path string) *fetch.SQLiteCache {
	cache, err := fetch.OpenSQLiteCache(s.T().Context(), path)
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(cache.Close()) })
	return cache
}

func (s *CacheSuite) TestStoresAndReadsBackAResponse() {
	cache := s.open(filepath.Join(s.T().TempDir(), "cache.db"))
	want := &fetch.Response{
		URL: "https://example.com/a", FinalURL: "https://example.com/b", Status: 200,
		ContentType: "text/html", ETag: `"v1"`, Body: []byte("<p>hi</p>"),
		SHA256: "abc", FetchedAt: stamp,
	}
	s.Require().NoError(cache.Put(s.T().Context(), want))

	got, ok, err := cache.Get(s.T().Context(), want.URL)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal(want, got)
	s.Empty(got.LastModified, "a header the server omitted reads back empty")

	_, ok, err = cache.Get(s.T().Context(), "https://example.com/missing")
	s.Require().NoError(err)
	s.False(ok)
}

func (s *CacheSuite) TestRobotsIsStoredPerHost() {
	cache := s.open(filepath.Join(s.T().TempDir(), "cache.db"))
	_, ok, err := cache.Robots(s.T().Context(), "example.com")
	s.Require().NoError(err)
	s.False(ok, "a host not yet asked")

	s.Require().NoError(cache.PutRobots(s.T().Context(), "example.com",
		&fetch.RobotsFile{Body: "User-agent: *\n", FetchedAt: stamp}))
	stored, ok, err := cache.Robots(s.T().Context(), "example.com")
	s.Require().NoError(err)
	s.True(ok)
	s.Equal("User-agent: *\n", stored.Body)
	// The age comes back, because the age is what decides whether the copy
	// may still say what the crawler fetches.
	s.Equal(stamp, stored.FetchedAt)
}

func (s *CacheSuite) TestACacheFromAnotherVersionIsRebuilt() {
	path := filepath.Join(s.T().TempDir(), "cache.db")
	cache := s.open(path)
	s.Require().NoError(cache.Put(s.T().Context(), &fetch.Response{
		URL: "https://example.com/a", FinalURL: "https://example.com/a", Status: 200,
		Body: []byte("old"), SHA256: "abc", FetchedAt: stamp,
	}))
	s.Require().NoError(cache.Close())
	s.setVersion(path, 99)

	// The cache is regenerable, so a file at another version is emptied
	// rather than migrated.
	rebuilt, err := fetch.OpenSQLiteCache(context.Background(), path)
	s.Require().NoError(err)
	defer func() { s.Require().NoError(rebuilt.Close()) }()
	_, ok, err := rebuilt.Get(s.T().Context(), "https://example.com/a")
	s.Require().NoError(err)
	s.False(ok)
}

func (s *CacheSuite) setVersion(path string, version int) {
	db, err := sql.Open("sqlite", path)
	s.Require().NoError(err)
	_, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version))
	s.Require().NoError(err)
	s.Require().NoError(db.Close())
}
