// Package pgupload keeps uploads and their signed links in Postgres.
//
// Uploads are tenant rows under the same row-level security as every other
// table, so one library's store reads only that library's files. Links are
// created by their issuer into a library, and redeemed before any user is
// known through the definer functions migrations 12 and 21 made.
package pgupload

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
	"github.com/bamsammich/docsearch/internal/upload"
)

// Store is upload.Store over one library's rows.
type Store struct {
	db    *sql.DB
	q     *pgdbgen.Queries
	scope pgsession.Scope
}

// New reads and writes one library's uploads through db.
func New(db *sql.DB, scope pgsession.Scope) *Store {
	return &Store{db: db, q: pgdbgen.New(db), scope: scope}
}

// Put stores a file, replacing whatever was under its key.
func (s *Store) Put(ctx context.Context, file upload.File) error {
	err := pgsession.Run(ctx, s.db, s.q, s.scope, func(q *pgdbgen.Queries) error {
		return q.PutUpload(ctx, pgdbgen.PutUploadParams{
			Key: file.Key, Filename: file.Filename, Sha256: file.SHA256, Content: file.Content,
		})
	})
	if err != nil {
		return fmt.Errorf("store upload %s: %w", file.Key, err)
	}
	return nil
}

// Get is the file under key.
func (s *Store) Get(ctx context.Context, key string) (upload.File, error) {
	row, err := pgsession.Read(ctx, s.db, s.q, s.scope,
		func(q *pgdbgen.Queries) (pgdbgen.GetUploadRow, error) {
			return q.GetUpload(ctx, key)
		})
	if errors.Is(err, sql.ErrNoRows) {
		return upload.File{}, fmt.Errorf("%s: %w", key, upload.ErrNotFound)
	}
	if err != nil {
		return upload.File{}, fmt.Errorf("read upload %s: %w", key, err)
	}
	return upload.File{
		Key: row.Key, Filename: row.Filename, SHA256: row.Sha256, Content: row.Content,
	}, nil
}

// Links is upload.Links on Postgres.
type Links struct {
	db *sql.DB
	q  *pgdbgen.Queries
}

// NewLinks keeps links through db.
func NewLinks(db *sql.DB) *Links {
	return &Links{db: db, q: pgdbgen.New(db)}
}

// Create records a link into dest's library, issued by dest's user, so the
// row is written under that library's policy like any other: only someone who
// can add to the library can issue a link into it.
func (l *Links) Create(
	ctx context.Context,
	dest upload.Destination,
	tokenHash string,
	expires time.Time,
) error {
	scope := pgsession.Scope{User: dest.User, Library: dest.Library}
	err := pgsession.Run(ctx, l.db, l.q, scope, func(q *pgdbgen.Queries) error {
		return q.CreateUploadLink(ctx, pgdbgen.CreateUploadLinkParams{
			TokenHash: tokenHash, ExpiresAt: expires,
		})
	})
	if err != nil {
		return fmt.Errorf("create upload link: %w", err)
	}
	return nil
}

// Redeem spends a link and names its library and issuer.
func (l *Links) Redeem(ctx context.Context, tokenHash string) (upload.Destination, bool, error) {
	rows, err := l.q.RedeemUploadLink(ctx, tokenHash)
	if err != nil {
		return upload.Destination{}, false, fmt.Errorf("redeem upload link: %w", err)
	}
	if len(rows) == 0 || rows[0].IssuedBy == "" {
		return upload.Destination{}, false, nil
	}
	return upload.Destination{User: rows[0].IssuedBy, Library: rows[0].LibraryID}, true, nil
}

// Open reports whether a link would still redeem.
func (l *Links) Open(ctx context.Context, tokenHash string) (bool, error) {
	open, err := l.q.UploadLinkOpen(ctx, tokenHash)
	if err != nil {
		return false, fmt.Errorf("check upload link: %w", err)
	}
	return open, nil
}
