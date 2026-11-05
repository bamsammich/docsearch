package connectapi

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore"
)

// ReadableFor is every library a user reads, their own first.
type ReadableFor func(ctx context.Context, user string) ([]pgstore.Library, error)

// shelf is one library a request reaches, and the scope that reads it.
type shelf struct {
	scope   pgsession.Scope
	library pgstore.Library
}

// labelled is the library a request named by its label, or the user's own
// when the label is empty.
//
// A label the user reads no library under is NotFound in the same words
// whether or not such a library exists.
func labelled(ctx context.Context, readable ReadableFor, label string) (shelf, error) {
	user, libs, err := readableOf(ctx, readable)
	if err != nil {
		return shelf{}, err
	}
	for _, lib := range libs {
		if (label == "" && lib.Own()) || (label != "" && lib.Label == label) {
			return shelf{library: lib, scope: pgsession.Scope{User: user, Library: lib.ID}}, nil
		}
	}
	return shelf{}, connect.NewError(connect.CodeNotFound,
		fmt.Errorf("you read no library named %q", label))
}

// holding resolves a doc_id: library/doc_id in a library the user reads, or
// a bare identifier in their own.
//
// An identifier naming a library the user does not read is looked up in
// their own as it was sent. No real identifier holds a slash, so it answers
// exactly as a document that does not exist.
func holding(ctx context.Context, readable ReadableFor, docID string) (shelf, string, error) {
	user, libs, err := readableOf(ctx, readable)
	if err != nil {
		return shelf{}, "", err
	}
	lib, local, ok := pgstore.Resolve(libs, docID)
	if !ok {
		lib, local = pgstore.Personal(user), docID
	}
	return shelf{library: lib, scope: pgsession.Scope{User: user, Library: lib.ID}}, local, nil
}

// editable refuses a library the user reads but cannot change.
func (s shelf) editable() error {
	if s.library.CanAdd() {
		return nil
	}
	return connect.NewError(
		connect.CodePermissionDenied,
		fmt.Errorf(
			"you read %s and cannot change it: ask one of its admins for editor",
			s.library.Label,
		),
	)
}

func readableOf(ctx context.Context, readable ReadableFor) (string, []pgstore.Library, error) {
	user, err := owner.From(ctx)
	if err != nil {
		return "", nil, asConnectError(err)
	}
	libs, err := readable(ctx, user)
	if err != nil {
		return "", nil, asConnectError(err)
	}
	return user, libs, nil
}
