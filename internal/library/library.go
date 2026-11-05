// Package library builds the view of one library, as one user sees it.
//
// Row-level security scopes a statement by the user and the library its
// transaction named, so a repository, a store and a crawl cache each belong
// to one scope and take it when they are constructed. A server serves many,
// and builds what a request needs once its user and library are known.
//
// The services themselves know nothing about any of it. A service reads a
// library; which library is a wiring question, and the answer lives here.
package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/inspect"
	"github.com/bamsammich/docsearch/internal/service/job"
	"github.com/bamsammich/docsearch/internal/site/fetch/pgcache"
	"github.com/bamsammich/docsearch/internal/source"
	"github.com/bamsammich/docsearch/internal/source/site"
	"github.com/bamsammich/docsearch/internal/upload/pgupload"
)

// Libraries builds each scope's view of one database.
//
// The pool is shared, because a connection pool is a property of the process
// rather than of a user. What each view carries is the scope every
// transaction it opens will name.
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

// Store is how one library is read: search, outline, documents and
// the context around a chunk.
func (l *Libraries) Store(scope pgsession.Scope) (*pgstore.Store, error) {
	if err := check(scope); err != nil {
		return nil, err
	}
	return pgstore.New(l.db, scope), nil
}

// Ready reports whether the database is openable and migrated.
//
// No user: the readiness probe answers before anyone has signed in, and
// neither table it reads carries a policy.
func (l *Libraries) Ready(ctx context.Context) error {
	return pgstore.New(l.db, pgsession.Scope{}).Ready(ctx)
}

// Documents is the document service over one library.
func (l *Libraries) Documents(scope pgsession.Scope) (*document.Service, error) {
	if err := check(scope); err != nil {
		return nil, err
	}
	return document.New(
		postgres.NewDocuments(l.db, scope),
		inspect.New(l.reader, l.formats, nil, l.roots),
	), nil
}

// Jobs is the ingest queue of one library, and the targets a request may name.
func (l *Libraries) Jobs(scope pgsession.Scope) (*job.Service, error) {
	if err := check(scope); err != nil {
		return nil, err
	}
	sources, err := l.Sources(scope)
	if err != nil {
		return nil, err
	}
	return job.New(postgres.NewJobs(l.db, scope), sources), nil
}

// Ingester writes into one library.
func (l *Libraries) Ingester(scope pgsession.Scope) (*ingest.Service, error) {
	if err := check(scope); err != nil {
		return nil, err
	}
	return ingest.New(postgres.New(l.db, scope), l.now), nil
}

// Sources turns a target into something to read, crawling through the crawl
// cache of the library it is read into.
func (l *Libraries) Sources(scope pgsession.Scope) (*source.Registry, error) {
	if err := check(scope); err != nil {
		return nil, err
	}
	return source.New(l.formats, l.roots, pgcache.New(l.db, scope),
		pgupload.New(l.db, scope), l.site), nil
}

// ErrNotWritable is a library a caller cannot add to: one they do not read,
// or one they read as a reader. Both answer alike.
var ErrNotWritable = errors.New(
	"you cannot add to that library: name one you edit, or leave library out for your own")

// Destination is the scope that adds to the library user named, or to their
// own when the name is empty.
func (l *Libraries) Destination(ctx context.Context, user, label string) (pgsession.Scope, error) {
	if user == "" {
		return pgsession.Scope{}, fmt.Errorf("resolve a library: %w", owner.ErrNoOwner)
	}
	if label == "" {
		return pgsession.Personal(user), nil
	}
	libs, err := l.Readable(ctx, user)
	if err != nil {
		return pgsession.Scope{}, err
	}
	lib, ok := pgstore.Writable(libs, label)
	if !ok {
		return pgsession.Scope{}, ErrNotWritable
	}
	return pgsession.Scope{User: user, Library: lib.ID}, nil
}

// Readable is every library user reads, their own first.
func (l *Libraries) Readable(ctx context.Context, user string) ([]pgstore.Library, error) {
	return pgstore.New(l.db, pgsession.Personal(user)).Libraries(ctx)
}

// Locate is the scope that reads a doc_id a caller sent, and the identifier
// within that library: library/doc_id in a library user reads, or a bare
// identifier in their own.
//
// An identifier naming a library user does not read is looked up in their
// own as it was sent. No real identifier holds a slash, so it answers exactly
// as a document that does not exist.
func (l *Libraries) Locate(
	ctx context.Context,
	user, docID string,
) (pgsession.Scope, string, error) {
	libs, err := l.Readable(ctx, user)
	if err != nil {
		return pgsession.Scope{}, "", err
	}
	lib, local, ok := pgstore.Resolve(libs, docID)
	if !ok {
		return pgsession.Personal(user), docID, nil
	}
	return pgsession.Scope{User: user, Library: lib.ID}, local, nil
}

// check refuses a view nobody owns.
//
// Built for an empty scope, every one of these would open transactions that
// name no user and match no rows, which reads as an empty library rather than
// as the wiring fault it is.
func check(scope pgsession.Scope) error {
	if scope.User == "" || scope.Library == "" {
		return fmt.Errorf("build a library view: %w", owner.ErrNoOwner)
	}
	return nil
}
