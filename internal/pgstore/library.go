package pgstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
)

// OwnLabel is how the user's own library is named to them.
const OwnLabel = "personal"

// Library is one library a user reads, as they see it.
type Library struct {
	// ID is the partition key. It never leaves the server, because a
	// shared library's id is an internal handle and a personal one is an
	// account id.
	ID string `json:"-"`
	// Label is what an agent types: the slug of a shared library, OwnLabel
	// for the user's own, and the id of someone else's personal library,
	// which has no slug.
	Label string `json:"library"`
	Name  string `json:"name,omitempty"`
	// Role is the user's own: reader, editor or admin.
	Role string `json:"role"`
	own  bool
}

// Own reports whether the library is the user's own.
func (l Library) Own() bool { return l.own }

// Qualify is the doc_id an agent uses for a document in this library: bare
// in the user's own library, so identifiers stored before libraries were
// shared still work, and label/doc_id everywhere else.
func (l Library) Qualify(docID string) string {
	if l.own {
		return docID
	}
	return l.Label + "/" + docID
}

// Libraries are every library the store's user reads, their own first.
func (s *Store) Libraries(ctx context.Context) ([]Library, error) {
	rows, err := pgsession.Read(ctx, s.db, s.q, pgsession.Personal(s.scope.User),
		func(q *pgdbgen.Queries) ([]pgdbgen.ReadableLibrariesRow, error) {
			return q.ReadableLibraries(ctx)
		})
	if err != nil {
		return nil, fmt.Errorf("list the readable libraries: %w", err)
	}
	out := make([]Library, 0, len(rows))
	for _, row := range rows {
		lib := Library{ID: row.LibraryID, Name: row.Name, Role: row.Role}
		switch {
		case row.LibraryID == s.scope.User:
			lib.Label, lib.own = OwnLabel, true
		case row.Slug != "":
			lib.Label = row.Slug
		default:
			lib.Label = row.LibraryID
		}
		out = append(out, lib)
	}
	return out, nil
}

// Resolve splits a doc_id an agent sent into the library it names and the
// identifier within it. A bare identifier is in the user's own library.
//
// A label the user reads no library under answers false, the same as a
// document that does not exist, so a probe cannot tell a library someone
// else holds from one that was never there.
func Resolve(libs []Library, docID string) (Library, string, bool) {
	label, local, qualified := strings.Cut(docID, "/")
	for _, lib := range libs {
		switch {
		case !qualified && lib.own:
			return lib, docID, true
		case qualified && lib.Label == label:
			return lib, local, true
		}
	}
	return Library{}, "", false
}
