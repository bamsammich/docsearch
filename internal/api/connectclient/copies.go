package connectclient

import (
	"context"

	"connectrpc.com/connect"

	documentv1 "github.com/bamsammich/docsearch/internal/api/docsearch/document/v1"
)

// Copied is a queued copy or move: the job writing it, and the identifier
// the document takes in its new library.
type Copied struct {
	DocID string
	JobID int64
}

// Copy queues a copy of docID into the library labelled library. as, when
// set, is the doc_id the copy takes there.
func (c *Client) Copy(ctx context.Context, docID, library, as string) (Copied, error) {
	res, err := c.documents.Copy(ctx, connect.NewRequest(&documentv1.CopyRequest{
		DocId: docID, Library: library, As: as,
	}))
	if err != nil {
		return Copied{}, err
	}
	return Copied{DocID: res.Msg.GetDocId(), JobID: res.Msg.GetJobId()}, nil
}

// Move queues a copy of docID into the library labelled library, and the
// removal of docID once the copy is ready.
func (c *Client) Move(ctx context.Context, docID, library, as string) (Copied, error) {
	res, err := c.documents.Move(ctx, connect.NewRequest(&documentv1.MoveRequest{
		DocId: docID, Library: library, As: as,
	}))
	if err != nil {
		return Copied{}, err
	}
	return Copied{DocID: res.Msg.GetDocId(), JobID: res.Msg.GetJobId()}, nil
}
