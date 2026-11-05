package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
	"github.com/bamsammich/docsearch/internal/service/worker"
)

// Queues finds the work a worker rotation serves.
//
// It runs before the worker knows whom to write as, and the request role
// sees only the libraries its user holds grants on, so it asks the two
// definer functions migration 19 made, on a bare connection.
type Queues struct {
	q *pgdbgen.Queries
}

// NewQueues reads the waiting queues through db.
func NewQueues(db *sql.DB) *Queues {
	return &Queues{q: pgdbgen.New(db)}
}

// Waiting fails the queued jobs nobody can be written as any longer, then
// names each library and requester that has work.
//
// Failing first means a job whose requester lost the right to add to its
// library is answered in the queue rather than left to wait for a claim
// that cannot come.
func (w *Queues) Waiting(ctx context.Context) ([]worker.Queue, error) {
	if _, err := w.q.FailUnclaimableJobs(ctx); err != nil {
		return nil, fmt.Errorf("fail jobs nobody can claim: %w", err)
	}
	rows, err := w.q.WaitingQueues(ctx)
	if err != nil {
		return nil, fmt.Errorf("list waiting queues: %w", err)
	}
	out := make([]worker.Queue, 0, len(rows))
	for _, row := range rows {
		out = append(out, worker.Queue{User: row.RequestedBy, Library: row.LibraryID})
	}
	return out, nil
}
