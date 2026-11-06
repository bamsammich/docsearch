package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/library"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/upload"
	"github.com/bamsammich/docsearch/internal/upload/pgupload"
)

// linkLifetime is how long a signed link works: long enough to find a file
// in a browser, short enough that one pasted into the wrong place soon stops
// mattering.
const linkLifetime = 15 * time.Minute

// uploads is both ways a file comes in, and the issuer of signed links.
type uploads struct {
	issuer *upload.Issuer
	doors  upload.Doors
}

func newUploads(
	cfg config.Config,
	db *sql.DB,
	libs *library.Libraries,
	formats *adapter.Registry,
	log *slog.Logger,
) uploads {
	links := pgupload.NewLinks(db)
	return uploads{
		issuer: upload.NewIssuer(links, cfg.PublicURL, linkLifetime, time.Now),
		doors: upload.Doors{
			Links: links,
			Stores: func(dest upload.Destination) (upload.Store, error) {
				return pgupload.New(db, scopeOf(dest)), nil
			},
			Enqueue:      enqueuer(libs),
			Destinations: destinations(libs),
			Supports:     formats.Supports,
			Log:          log,
			MaxBytes:     cfg.UploadMaxMB << 20,
		},
	}
}

// errNothingQueued is an enqueue that answered with no job, which a single
// upload target never should.
var errNothingQueued = errors.New("the upload was stored but no job was queued")

// scopeOf is the transaction scope an upload into dest is written in.
func scopeOf(dest upload.Destination) pgsession.Scope {
	return pgsession.Scope{User: dest.User, Library: dest.Library}
}

// destinations resolves the library a program named on the upload door.
func destinations(
	libs *library.Libraries,
) func(context.Context, string, string) (upload.Destination, error) {
	return func(ctx context.Context, user, label string) (upload.Destination, error) {
		scope, err := libs.Destination(ctx, user, label)
		if errors.Is(err, library.ErrNotWritable) {
			return upload.Destination{}, upload.ErrNoDestination
		}
		if err != nil {
			return upload.Destination{}, err
		}
		return upload.Destination{User: scope.User, Library: scope.Library}, nil
	}
}

// enqueuer queues one target into a destination and names the job.
func enqueuer(
	libs *library.Libraries,
) func(context.Context, upload.Destination, string, string) (int64, error) {
	return func(ctx context.Context, dest upload.Destination, target, title string) (int64, error) {
		jobs, err := libs.Jobs(scopeOf(dest))
		if err != nil {
			return 0, err
		}
		queued, err := jobs.Enqueue(ctx, target, title, false)
		if err != nil {
			return 0, err
		}
		if len(queued) == 0 {
			return 0, errNothingQueued
		}
		return queued[0].JobID, nil
	}
}

// newUploadRoute is the authenticated door, for a program holding a token.
func newUploadRoute(ups uploads) route {
	return route{path: "/upload", handler: ups.doors.Direct()}
}
