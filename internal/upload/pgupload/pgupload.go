// Package pgupload keeps uploads and their signed links in Postgres.
//
// Uploads are tenant rows under the same row-level security as every other
// table, so one user's store reads only that user's files. Links are created
// as their user, and redeemed before any user is known through the definer
// functions migration 12 made.
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

// Store is upload.Store over one user's rows.
type Store struct {
	db     *sql.DB
	q      *pgdbgen.Queries
	userID string
}

// New reads and writes userID's uploads through db.
func New(db *sql.DB, userID string) *Store {
	return &Store{db: db, q: pgdbgen.New(db), userID: userID}
}

// Put stores a file, replacing whatever was under its key.
func (s *Store) Put(ctx context.Context, file upload.File) error {
	err := pgsession.Run(ctx, s.db, s.q, s.userID, func(q *pgdbgen.Queries) error {
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
	row, err := pgsession.Read(ctx, s.db, s.q, s.userID,
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

// Create records a link as its user, so the row is written under that
// user's policy like any other.
func (l *Links) Create(ctx context.Context, user, tokenHash string, expires time.Time) error {
	err := pgsession.Run(ctx, l.db, l.q, user, func(q *pgdbgen.Queries) error {
		return q.CreateUploadLink(ctx, pgdbgen.CreateUploadLinkParams{
			TokenHash: tokenHash, ExpiresAt: expires,
		})
	})
	if err != nil {
		return fmt.Errorf("create upload link: %w", err)
	}
	return nil
}

// Redeem spends a link and names its user.
func (l *Links) Redeem(ctx context.Context, tokenHash string) (string, bool, error) {
	user, err := l.q.RedeemUploadLink(ctx, tokenHash)
	if err != nil {
		return "", false, fmt.Errorf("redeem upload link: %w", err)
	}
	return user, user != "", nil
}

// Open reports whether a link would still redeem.
func (l *Links) Open(ctx context.Context, tokenHash string) (bool, error) {
	open, err := l.q.UploadLinkOpen(ctx, tokenHash)
	if err != nil {
		return false, fmt.Errorf("check upload link: %w", err)
	}
	return open, nil
}
