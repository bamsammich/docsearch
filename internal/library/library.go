// Package library builds the view of one user's library.
//
// Row-level security scopes a statement by the user its transaction named, so
// a repository, a store and a crawl cache each belong to one user and take
// that user when they are constructed. A server serves many, and builds what
// a request needs once its owner is known.
//
// The services themselves know nothing about any of it. A service reads a
// library; which library is a wiring question, and the answer lives here.
package library

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/pgstore"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/inspect"
	"github.com/bamsammich/docsearch/internal/service/job"
	"github.com/bamsammich/docsearch/internal/site/fetch/pgcache"
	"github.com/bamsammich/docsearch/internal/source"
	"github.com/bamsammich/docsearch/internal/source/site"
)

// Libraries builds each user's view of one database.
//
// The pool is shared, because a connection pool is a property of the process
// rather than of a user. What each view carries is the user every transaction
// it opens will name.
type Libraries struct {
	db *sql.DB
	// reader reads a PDF's primitives for reconnaissance, where formats
	// both says which files an adapter reads and does the reading.
	reader  inspect.Reader
	formats *adapter.Registry
	// now is the clock the ingest service stamps a document with, taken as
	// a dependency so a test can hold it still.
	now func() time.Time
	// roots bound which files an ingest may read, the same for every user:
	// a deployment offers the directories it offers.
	roots []string
	site  site.Options
}

// New builds the libraries one database holds.
func New(
	db *sql.DB,
	reader inspect.Reader,
	formats *adapter.Registry,
	roots []string,
	siteOptions site.Options,
	now func() time.Time,
) *Libraries {
	return &Libraries{
		db: db, reader: reader, formats: formats,
		roots: roots, site: siteOptions, now: now,
	}
}

// Store is how one user's library is read: search, outline, documents and
// the context around a chunk.
func (l *Libraries) Store(user string) (*pgstore.Store, error) {
	if err := check(user); err != nil {
		return nil, err
	}
	return pgstore.New(l.db, user), nil
}

// Ready reports whether the database is openable and migrated.
//
// No user: the readiness probe answers before anyone has signed in, and
// neither table it reads carries a policy.
func (l *Libraries) Ready(ctx context.Context) error {
	return pgstore.New(l.db, "").Ready(ctx)
}

// Documents is the document service over one user's library.
func (l *Libraries) Documents(user string) (*document.Service, error) {
	if err := check(user); err != nil {
		return nil, err
	}
	return document.New(
		postgres.NewDocuments(l.db, user),
		inspect.New(l.reader, l.formats, nil),
	), nil
}

// Jobs is the ingest queue of one user, and the targets a request may name.
func (l *Libraries) Jobs(user string) (*job.Service, error) {
	if err := check(user); err != nil {
		return nil, err
	}
	sources, err := l.Sources(user)
	if err != nil {
		return nil, err
	}
	return job.New(postgres.NewJobs(l.db, user), sources), nil
}

// Ingester writes into one user's library.
func (l *Libraries) Ingester(user string) (*ingest.Service, error) {
	if err := check(user); err != nil {
		return nil, err
	}
	return ingest.New(postgres.New(l.db, user), l.now), nil
}

// Sources turns a target into something to read, crawling through the crawl
// cache of the user asking.
func (l *Libraries) Sources(user string) (*source.Registry, error) {
	if err := check(user); err != nil {
		return nil, err
	}
	return source.New(l.formats, l.roots, pgcache.New(l.db, user), l.site), nil
}

// check refuses a view nobody owns.
//
// Built for the empty user, every one of these would open transactions that
// name no user and match no rows, which reads as an empty library rather than
// as the wiring fault it is.
func check(user string) error {
	if user == "" {
		return fmt.Errorf("build a library view: %w", owner.ErrNoOwner)
	}
	return nil
}
