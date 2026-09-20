package fetch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, as the store uses
)

// cacheVersion is bumped when the shape changes; a file at another version
// is rebuilt. The cache is raw HTTP responses, disposable and regenerable,
// so dropping it costs bandwidth and nothing else.
const cacheVersion = 1

const cacheSchema = `
CREATE TABLE IF NOT EXISTS responses (
  url            TEXT PRIMARY KEY,   -- normalized request URL
  final_url      TEXT NOT NULL,      -- after redirects; equals url when none
  status         INTEGER NOT NULL,
  content_type   TEXT,
  etag           TEXT,
  last_modified  TEXT,
  body           BLOB NOT NULL,
  sha256         TEXT NOT NULL,
  fetched_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS robots (
  host        TEXT PRIMARY KEY,
  body        TEXT NOT NULL,        -- empty when the host serves no robots.txt
  fetched_at  TEXT NOT NULL
);
`

// Response is one stored HTTP response.
type Response struct {
	URL          string
	FinalURL     string
	ContentType  string
	ETag         string
	LastModified string
	SHA256       string
	FetchedAt    string
	Body         []byte
	Status       int
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
	Robots(ctx context.Context, host string) (string, bool, error)
	// PutRobots stores a host's robots.txt.
	PutRobots(ctx context.Context, host, body string) error
}

// SQLiteCache is a Cache in one SQLite file, the shape the Python worker
// writes, so either can read the other's. Phase 04 moves the search index to
// Postgres and decides where this belongs: a worker in a container loses a
// local file when it restarts, which is most of what the cache is for.
type SQLiteCache struct {
	db *sql.DB
}

// OpenSQLiteCache opens the cache at path, creating it, and rebuilds it when
// it was written by another version.
func OpenSQLiteCache(ctx context.Context, path string) (*SQLiteCache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("cache directory: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("open cache %s: %w", path, err)
	}
	cache := &SQLiteCache{db: db}
	if err := cache.migrate(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return cache, nil
}

func (c *SQLiteCache) migrate(ctx context.Context) error {
	var found int
	if err := c.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&found); err != nil {
		return fmt.Errorf("read cache version: %w", err)
	}
	if found != 0 && found != cacheVersion {
		if err := c.dropTables(ctx); err != nil {
			return err
		}
		found = 0
	}
	if _, err := c.db.ExecContext(ctx, cacheSchema); err != nil {
		return fmt.Errorf("create cache tables: %w", err)
	}
	if found == cacheVersion {
		return nil
	}
	if _, err := c.db.ExecContext(
		ctx,
		fmt.Sprintf("PRAGMA user_version=%d", cacheVersion),
	); err != nil {
		return fmt.Errorf("set cache version: %w", err)
	}
	return nil
}

// dropTables empties a cache written by another version.
func (c *SQLiteCache) dropTables(ctx context.Context) error {
	if _, err := c.db.ExecContext(ctx,
		"DROP TABLE IF EXISTS responses; DROP TABLE IF EXISTS robots;"); err != nil {
		return fmt.Errorf("drop stale cache: %w", err)
	}
	return nil
}

// Close closes the cache.
func (c *SQLiteCache) Close() error {
	if err := c.db.Close(); err != nil {
		return fmt.Errorf("close cache: %w", err)
	}
	return nil
}

// Get is the stored response for url, and whether one was stored.
func (c *SQLiteCache) Get(ctx context.Context, url string) (*Response, bool, error) {
	row := c.db.QueryRowContext(ctx, `SELECT url, final_url, status, content_type, etag,
		last_modified, body, sha256, fetched_at FROM responses WHERE url = ?`, url)
	var (
		r                          Response
		contentType, etag, lastMod sql.NullString
	)
	err := row.Scan(&r.URL, &r.FinalURL, &r.Status, &contentType, &etag, &lastMod,
		&r.Body, &r.SHA256, &r.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s from cache: %w", url, err)
	}
	r.ContentType, r.ETag, r.LastModified = contentType.String, etag.String, lastMod.String
	return &r, true, nil
}

// Put stores a response, replacing any stored under the same URL.
func (c *SQLiteCache) Put(ctx context.Context, r *Response) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO responses (url, final_url, status,
		content_type, etag, last_modified, body, sha256, fetched_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(url) DO UPDATE SET final_url=excluded.final_url, status=excluded.status,
		content_type=excluded.content_type, etag=excluded.etag,
		last_modified=excluded.last_modified, body=excluded.body, sha256=excluded.sha256,
		fetched_at=excluded.fetched_at`,
		r.URL, r.FinalURL, r.Status, nullable(r.ContentType), nullable(r.ETag),
		nullable(r.LastModified), r.Body, r.SHA256, r.FetchedAt)
	if err != nil {
		return fmt.Errorf("store %s in cache: %w", r.URL, err)
	}
	return nil
}

// Touch records that a conditional request confirmed the stored copy.
func (c *SQLiteCache) Touch(ctx context.Context, url string) error {
	if _, err := c.db.ExecContext(ctx,
		"UPDATE responses SET fetched_at = ? WHERE url = ?", now(), url); err != nil {
		return fmt.Errorf("touch %s in cache: %w", url, err)
	}
	return nil
}

// Robots is the stored robots.txt for host, and whether one was stored. An
// empty body means the host serves none, which is permission.
func (c *SQLiteCache) Robots(ctx context.Context, host string) (string, bool, error) {
	var body string
	err := c.db.QueryRowContext(ctx, "SELECT body FROM robots WHERE host = ?", host).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read robots for %s: %w", host, err)
	}
	return body, true, nil
}

// PutRobots stores a host's robots.txt.
func (c *SQLiteCache) PutRobots(ctx context.Context, host, body string) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO robots (host, body, fetched_at) VALUES (?,?,?)
		ON CONFLICT(host) DO UPDATE SET body=excluded.body, fetched_at=excluded.fetched_at`,
		host, body, now())
	if err != nil {
		return fmt.Errorf("store robots for %s: %w", host, err)
	}
	return nil
}

// nullable writes "" as SQL NULL, so a column Python left NULL reads back
// the same way.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// now is the timestamp format the Python cache writes.
func now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}
