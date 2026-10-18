package main

import (
	"database/sql"
	"log/slog"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/library"
	"github.com/bamsammich/docsearch/internal/original"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/upload/pgupload"
)

// newOriginalRoute serves the file a document was read from, behind the same
// guard as every other route.
func newOriginalRoute(
	cfg config.Config,
	db *sql.DB,
	libs *library.Libraries,
	log *slog.Logger,
) route {
	return route{path: original.Pattern, handler: original.Handler{
		Documents: func(user string) (original.Documents, error) { return libs.Documents(user) },
		Uploads: func(user string) (original.Uploads, error) {
			return pgupload.New(db, user), nil
		},
		NotFound: postgres.ErrNotFound,
		Log:      log,
		Roots:    cfg.LibraryRoots,
	}}
}
