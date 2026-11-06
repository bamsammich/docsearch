package connectapi_test

import (
	"context"

	"github.com/bamsammich/docsearch/internal/pgstore"
)

// Library ids the suites' user reads besides their own.
const (
	platformID = "lib-platform"
	runbooksID = "lib-runbooks"
)

// shelves is what every suite's user reads: their own library, one they
// edit and one they only read.
func shelves(_ context.Context, user string) ([]pgstore.Library, error) {
	return []pgstore.Library{
		pgstore.Personal(user),
		{ID: platformID, Label: "platform", Name: "Platform docs", Role: "editor"},
		{ID: runbooksID, Label: "runbooks", Name: "Runbooks", Role: "reader"},
	}, nil
}
