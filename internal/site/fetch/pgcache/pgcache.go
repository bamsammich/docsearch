// Package pgcache keeps a crawl's responses in Postgres, beside the index.
//
// A worker in a container loses a local file when it restarts, and surviving
// a restart is most of what a response cache is for: a cancelled crawl
// resumes, a refresh sends conditional requests, and re-chunking a site makes
// none. internal/site/fetch.SQLiteCache is the same contract over a file, and
// step 4g of docs/plans/postgres-multiuser.md removes it.
//
// A cache belongs to one user, as every other table in the database does.
// Which hosts someone crawled is not public even where the responses are.
package pgcache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/store/pgdbgen"
)

// Cache is a fetch.Cache over one user's rows in a Postgres database.
type Cache struct {
	db *sql.DB
	q  *pgdbgen.Queries
	// userID owns every row read or written. Bound once, because a cache
	// serves one crawl and a crawl belongs to one user.
	userID string
}

// New caches one user's responses through db, which the caller opens and
// closes. The worker already holds a connection pool for the index, and the
// cache has no reason to open a second.
func New(db *sql.DB, userID string) *Cache {
	return &Cache{db: db, q: pgdbgen.New(db), userID: userID}
}

// Get is the stored response for url, and whether one was stored.
func (c *Cache) Get(ctx context.Context, url string) (*fetch.Response, bool, error) {
	row, err := pgsession.Read(ctx, c.db, c.q, c.userID,
		func(q *pgdbgen.Queries) (pgdbgen.CachedResponseRow, error) {
			return q.CachedResponse(ctx, url)
		})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s from cache: %w", url, err)
	}
	return &fetch.Response{
		URL:          row.Url,
		FinalURL:     row.FinalUrl,
		ContentType:  row.ContentType.String,
		ETag:         row.Etag.String,
		LastModified: row.LastModified.String,
		SHA256:       row.Sha256,
		FetchedAt:    row.FetchedAt.UTC(),
		Body:         row.Body,
		Status:       int(row.Status),
	}, true, nil
}

// Put stores a response, replacing any stored under the same URL.
func (c *Cache) Put(ctx context.Context, r *fetch.Response) error {
	err := pgsession.Run(ctx, c.db, c.q, c.userID, func(q *pgdbgen.Queries) error {
		return q.StoreResponse(ctx, pgdbgen.StoreResponseParams{
			UserID:       c.userID,
			Url:          r.URL,
			FinalUrl:     r.FinalURL,
			Status:       pgsession.Narrow(r.Status),
			ContentType:  nullable(r.ContentType),
			Etag:         nullable(r.ETag),
			LastModified: nullable(r.LastModified),
			Body:         r.Body,
			Sha256:       r.SHA256,
			FetchedAt:    r.FetchedAt.UTC(),
		})
	})
	if err != nil {
		return fmt.Errorf("store %s in cache: %w", r.URL, err)
	}
	return nil
}

// Touch records that a conditional request confirmed the stored copy.
func (c *Cache) Touch(ctx context.Context, url string) error {
	err := pgsession.Run(ctx, c.db, c.q, c.userID, func(q *pgdbgen.Queries) error {
		return q.TouchResponse(ctx, url)
	})
	if err != nil {
		return fmt.Errorf("touch %s in cache: %w", url, err)
	}
	return nil
}

// Robots is the stored robots.txt for host, and whether one was stored. An
// empty body means the host serves none, which is permission.
func (c *Cache) Robots(ctx context.Context, host string) (*fetch.RobotsFile, bool, error) {
	row, err := pgsession.Read(ctx, c.db, c.q, c.userID,
		func(q *pgdbgen.Queries) (pgdbgen.CachedRobotsRow, error) {
			return q.CachedRobots(ctx, host)
		})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read robots for %s: %w", host, err)
	}
	return &fetch.RobotsFile{Body: row.Body, FetchedAt: row.FetchedAt.UTC()}, true, nil
}

// PutRobots stores a host's robots.txt.
func (c *Cache) PutRobots(ctx context.Context, host string, r *fetch.RobotsFile) error {
	err := pgsession.Run(ctx, c.db, c.q, c.userID, func(q *pgdbgen.Queries) error {
		return q.StoreRobots(ctx, pgdbgen.StoreRobotsParams{
			UserID: c.userID, Host: host, Body: r.Body, FetchedAt: r.FetchedAt.UTC(),
		})
	})
	if err != nil {
		return fmt.Errorf("store robots for %s: %w", host, err)
	}
	return nil
}

// nullable writes "" as SQL NULL, so a header the server omitted reads back
// empty rather than as an empty string the column claims it sent.
func nullable(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
