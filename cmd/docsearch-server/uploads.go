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
			Stores: func(user string) (upload.Store, error) {
				return pgupload.New(db, user), nil
			},
			Enqueue:  enqueuer(libs),
			Supports: formats.Supports,
			Log:      log,
			MaxBytes: cfg.UploadMaxMB << 20,
		},
	}
}

// errNothingQueued is an enqueue that answered with no job, which a single
// upload target never should.
var errNothingQueued = errors.New("the upload was stored but no job was queued")

// enqueuer queues one target into a user's library and names the job.
func enqueuer(
	libs *library.Libraries,
) func(context.Context, string, string, string) (int64, error) {
	return func(ctx context.Context, user, target, title string) (int64, error) {
		jobs, err := libs.Jobs(user)
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
