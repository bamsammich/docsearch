package pgcache_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/site/fetch/pgcache"
)

// stamp is when the stored responses in these cases were read from a host.
var stamp = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)

// builtinUser owns the rows these cases write, which is the one user phase 04
// serves until authentication arrives.
const builtinUser = "default"

// CacheSuite checks the response cache against a real database, because every
// claim it makes is about what row-level security and an upsert do.
type CacheSuite struct {
	suite.Suite
	pg *pgtest.DB
}

func TestCache(t *testing.T) { suite.Run(t, new(CacheSuite)) }

func (s *CacheSuite) SetupTest() {
	s.pg = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.pg.Owner))
}

// cache reads as the restricted role a request uses, not as the owner the
// migrations ran under.
func (s *CacheSuite) cache(userID string) *pgcache.Cache {
	return pgcache.New(s.pg.App, userID)
}

func response(url, body string) *fetch.Response {
	return &fetch.Response{
		URL: url, FinalURL: url, Status: 200, ContentType: "text/html",
		ETag: `"v1"`, Body: []byte(body), SHA256: "abc",
		FetchedAt: stamp,
	}
}

func (s *CacheSuite) TestStoresAndReadsBackAResponse() {
	cache := s.cache(builtinUser)
	want := response("https://example.com/a", "<p>hi</p>")
	want.FinalURL = "https://example.com/b"
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

// A second fetch of one URL replaces what was stored for it, which is what
// makes a refresh idempotent rather than a primary-key violation.
func (s *CacheSuite) TestRefetchingAURLReplacesTheStoredResponse() {
	cache := s.cache(builtinUser)
	ctx := s.T().Context()
	s.Require().NoError(cache.Put(ctx, response("https://example.com/a", "old")))
	s.Require().NoError(cache.Put(ctx, response("https://example.com/a", "new")))

	got, ok, err := cache.Get(ctx, "https://example.com/a")
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal("new", string(got.Body))
}

// Touch answers a 304, so it must move the age of a stored copy without
// touching the copy itself.
func (s *CacheSuite) TestTouchKeepsTheBodyAndMovesTheClock() {
	cache := s.cache(builtinUser)
	ctx := s.T().Context()
	stored := response("https://example.com/a", "<p>hi</p>")
	s.Require().NoError(cache.Put(ctx, stored))

	s.Require().NoError(cache.Touch(ctx, stored.URL))
	got, ok, err := cache.Get(ctx, stored.URL)
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal(stored.Body, got.Body)
	s.NotEqual(stored.FetchedAt, got.FetchedAt, "a confirmed copy is no longer that old")
}

func (s *CacheSuite) TestRobotsIsStoredPerHost() {
	cache := s.cache(builtinUser)
	ctx := s.T().Context()
	_, ok, err := cache.Robots(ctx, "example.com")
	s.Require().NoError(err)
	s.False(ok, "a host not yet asked")

	s.Require().NoError(cache.PutRobots(ctx, "example.com",
		&fetch.RobotsFile{Body: "User-agent: *\n", FetchedAt: stamp}))
	stored, ok, err := cache.Robots(ctx, "example.com")
	s.Require().NoError(err)
	s.True(ok)
	s.Equal("User-agent: *\n", stored.Body)
	// The age survives the round trip, because the crawler decides on it
	// whether the copy may still say what it fetches.
	s.Equal(stamp, stored.FetchedAt)
}

// Which hosts someone crawled is private even though the responses are
// public, so one user's cache is invisible to another and neither overwrites
// the other's row for the same URL.
func (s *CacheSuite) TestOneUsersCacheIsInvisibleToAnother() {
	ctx := s.T().Context()
	_, err := s.pg.Owner.ExecContext(ctx, `INSERT INTO users (user_id) VALUES ('second')`)
	s.Require().NoError(err)

	mine, theirs := s.cache(builtinUser), s.cache("second")
	s.Require().NoError(mine.Put(ctx, response("https://example.com/a", "mine")))
	s.Require().NoError(theirs.Put(ctx, response("https://example.com/a", "theirs")))
	s.Require().NoError(mine.PutRobots(ctx, "example.com",
		&fetch.RobotsFile{Body: "mine", FetchedAt: stamp}))

	got, ok, err := mine.Get(ctx, "https://example.com/a")
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal("mine", string(got.Body))

	_, ok, err = theirs.Robots(ctx, "example.com")
	s.Require().NoError(err)
	s.False(ok, "a host the other user asked about")
}

// The reason the cache moved into the database: a worker that restarts keeps
// what it fetched, so a cancelled crawl resumes instead of starting over.
func (s *CacheSuite) TestACrawlResumesInANewProcess() {
	ctx := s.T().Context()
	s.Require().NoError(s.cache(builtinUser).Put(ctx,
		response("https://example.com/a", "<p>hi</p>")))

	// A separate pool, as a restarted worker opens: nothing of the first
	// cache survives except what it wrote.
	restarted, err := sql.Open("pgx", s.pg.AppDSN)
	s.Require().NoError(err)
	defer func() { s.Require().NoError(restarted.Close()) }()

	got, ok, err := pgcache.New(restarted, builtinUser).Get(ctx, "https://example.com/a")
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Equal("<p>hi</p>", string(got.Body))
}

// A statement without an owner matches no rows, so the cache refuses rather
// than reporting an empty cache that a crawl would fetch all over again.
func (s *CacheSuite) TestACacheWithNoUserRefuses() {
	_, _, err := s.cache("").Get(s.T().Context(), "https://example.com/a")
	s.Require().Error(err)
	s.Contains(err.Error(), "no user")
}
