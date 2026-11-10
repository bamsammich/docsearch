package connectapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	documentv1 "github.com/bamsammich/docsearch/internal/api/docsearch/document/v1"
	"github.com/bamsammich/docsearch/internal/service/document"
)

// Copies starts copies and moves of documents between libraries.
// internal/repository/postgres.Copies satisfies it.
type Copies interface {
	Start(ctx context.Context, req document.CopyRequest) (int64, error)
}

// CopiesFor builds the copier for one user.
type CopiesFor func(user string) (Copies, error)

// copyOrder is a copy or a move as a request sent it.
type copyOrder struct {
	docID string
	label string
	as    string
	move  bool
}

func (s *DocumentServer) Copy(
	ctx context.Context,
	req *connect.Request[documentv1.CopyRequest],
) (*connect.Response[documentv1.CopyResponse], error) {
	job, docID, err := s.start(ctx, copyOrder{
		docID: req.Msg.GetDocId(), label: req.Msg.GetLibrary(), as: req.Msg.GetAs(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&documentv1.CopyResponse{JobId: job, DocId: docID}), nil
}

func (s *DocumentServer) Move(
	ctx context.Context,
	req *connect.Request[documentv1.MoveRequest],
) (*connect.Response[documentv1.MoveResponse], error) {
	job, docID, err := s.start(ctx, copyOrder{
		docID: req.Msg.GetDocId(), label: req.Msg.GetLibrary(), as: req.Msg.GetAs(), move: true,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&documentv1.MoveResponse{JobId: job, DocId: docID}), nil
}

// start queues a copy or a move and returns its job and the identifier the
// document takes in its new library.
//
// Roles are checked here against the libraries the person reads, for a
// refusal that names what to ask for, and again by the row policies in the
// transaction that starts the copy.
func (s *DocumentServer) start(ctx context.Context, order copyOrder) (int64, string, error) {
	if err := order.check(); err != nil {
		return 0, "", err
	}
	ends, err := s.ends(ctx, order)
	if err != nil {
		return 0, "", err
	}
	copies, err := s.copies(ends.from.scope.User)
	if err != nil {
		return 0, "", asConnectError(err)
	}
	req := document.CopyRequest{
		From: ends.from.library.ID, DocID: ends.local, To: ends.to.library.ID,
		As: order.as, Move: order.move,
	}
	job, err := copies.Start(ctx, req)
	if err != nil {
		return 0, "", s.asCopyError(err)
	}
	return job, ends.to.library.Qualify(req.Target()), nil
}

// copyEnds are the library a copy leaves, the doc_id inside it, and the
// library the copy goes to.
type copyEnds struct {
	local string
	from  shelf
	to    shelf
}

// ends resolves where a copy leaves and goes, refusing a pair the person's
// roles do not allow.
func (s *DocumentServer) ends(ctx context.Context, order copyOrder) (copyEnds, error) {
	from, local, err := holding(ctx, s.readable, order.docID)
	if err != nil {
		return copyEnds{}, err
	}
	to, err := labelled(ctx, s.readable, order.label)
	if err != nil {
		return copyEnds{}, err
	}
	if from.library.ID == to.library.ID {
		return copyEnds{}, connect.NewError(connect.CodeInvalidArgument, document.ErrSameLibrary)
	}
	if order.move {
		if err := from.editable(); err != nil {
			return copyEnds{}, err
		}
	}
	return copyEnds{from: from, to: to, local: local}, to.editable()
}

func (o copyOrder) check() error {
	switch {
	case o.docID == "":
		return connect.NewError(connect.CodeInvalidArgument,
			errors.New("doc_id names the document to copy"))
	case o.label == "":
		return connect.NewError(connect.CodeInvalidArgument,
			errors.New("library names where the copy goes"))
	case o.as != "" && !document.ValidDocID(o.as):
		return connect.NewError(connect.CodeInvalidArgument, document.ErrDocIDForm)
	}
	return nil
}

// asCopyError says what the person can do about a refused copy.
func (s *DocumentServer) asCopyError(err error) error {
	var (
		taken  document.Taken
		held   document.AlreadyHeld
		source document.SourceHeld
	)
	switch {
	case errors.As(err, &taken), errors.As(err, &held), errors.As(err, &source):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, document.ErrCannotMove), errors.Is(err, document.ErrCannotAdd):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, document.ErrNotReady), errors.Is(err, document.ErrOriginalGone):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return s.asConnectError(err)
	}
}
