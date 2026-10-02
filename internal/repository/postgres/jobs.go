package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/service/job"
	"github.com/bamsammich/docsearch/internal/service/worker"
	"github.com/bamsammich/docsearch/internal/store/pgdbgen"
)

// Jobs is the ingest queue, in the same database the documents are in.
//
// Sharing the database is what lets a job be completed in the transaction
// that makes its document visible, so a document can never be searchable
// while its job still reads as running.
type Jobs struct {
	db *sql.DB
	q  *pgdbgen.Queries
	// userID is the library every statement here reads and writes. Bound
	// once, because a repository serves one request and a request belongs
	// to one user.
	userID string
}

// NewJobs reads and writes one user's queue through db.
//
// The user is bound here rather than passed per call, because a repository
// serves one request and a request belongs to one user. Step 4f hands it
// down from the service layer; until then it is the built-in user.
func NewJobs(db *sql.DB, userID string) *Jobs {
	return &Jobs{db: db, q: pgdbgen.New(db), userID: userID}
}

// Claim takes the next job. A single statement, so two workers racing cannot
// take the same row.
func (j *Jobs) Claim(
	ctx context.Context,
	lease time.Duration,
	maxAttempts int,
) (*worker.Job, error) {
	var row pgdbgen.IngestJob
	err := j.inTx(ctx, func(q *pgdbgen.Queries) error {
		var claimErr error
		row, claimErr = q.ClaimJob(ctx, pgdbgen.ClaimJobParams{
			LeaseSeconds: lease.Seconds(),
			MaxAttempts:  narrow(maxAttempts),
		})
		return claimErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // an empty queue is not a failure
	}
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	return &worker.Job{
		Title:    row.Title.String,
		Source:   row.SourcePath,
		ID:       row.ID,
		Attempts: int(row.Attempts),
	}, nil
}

func (j *Jobs) RecordProgress(
	ctx context.Context,
	id int64,
	phase string,
	cur, tot int,
	lease time.Duration,
) error {
	err := j.inTx(ctx, func(q *pgdbgen.Queries) error {
		return q.RecordJobProgress(ctx, pgdbgen.RecordJobProgressParams{
			Phase:        nullString(phase),
			ProgressCur:  sql.NullInt32{Int32: narrow(cur), Valid: true},
			ProgressTot:  sql.NullInt32{Int32: narrow(tot), Valid: true},
			LeaseSeconds: lease.Seconds(),
			ID:           id,
		})
	})
	if err != nil {
		return fmt.Errorf("write progress for job %d: %w", id, err)
	}
	return nil
}

func (j *Jobs) RecordDocID(ctx context.Context, id int64, docID string) error {
	err := j.inTx(ctx, func(q *pgdbgen.Queries) error {
		return q.RecordJobDocID(ctx, pgdbgen.RecordJobDocIDParams{
			DocID: nullString(docID),
			ID:    id,
		})
	})
	if err != nil {
		return fmt.Errorf("record the document of job %d: %w", id, err)
	}
	return nil
}

func (j *Jobs) CancelRequested(ctx context.Context, id int64) (bool, error) {
	requested, err := read(ctx, j.db, j.q, j.userID, func(q *pgdbgen.Queries) (bool, error) {
		return q.JobCancelRequested(ctx, id)
	})
	if errors.Is(err, sql.ErrNoRows) {
		// The row is gone, so nothing is waiting for the job either.
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the cancel request for job %d: %w", id, err)
	}
	return requested, nil
}

// Cancel removes whatever the job half wrote and marks it cancelled, in one
// transaction.
func (j *Jobs) Cancel(ctx context.Context, id int64) error {
	return j.inTx(ctx, func(q *pgdbgen.Queries) error {
		if err := dropPartial(ctx, q, id); err != nil {
			return err
		}
		return q.CancelJob(ctx, id)
	})
}

// Finish puts a job back on the queue or leaves it failed, having removed
// whatever it half wrote.
func (j *Jobs) Finish(
	ctx context.Context,
	id int64,
	outcome worker.Outcome,
	reason string,
	permanent bool,
) error {
	status := "queued"
	if outcome == worker.Failed {
		status = "failed"
	}
	return j.inTx(ctx, func(q *pgdbgen.Queries) error {
		if err := dropPartial(ctx, q, id); err != nil {
			return err
		}
		return q.FinishJob(ctx, pgdbgen.FinishJobParams{
			Status:    status,
			Error:     nullString(reason),
			Permanent: permanent,
			ID:        id,
		})
	})
}

func (j *Jobs) CompleteWithoutDocument(ctx context.Context, id int64, docID string) error {
	err := j.inTx(ctx, func(q *pgdbgen.Queries) error {
		return q.CompleteJobWithoutDocument(ctx, pgdbgen.CompleteJobWithoutDocumentParams{
			DocID: nullString(docID),
			ID:    id,
		})
	})
	if err != nil {
		return fmt.Errorf("complete job %d: %w", id, err)
	}
	return nil
}

// dropPartial removes the document a job left mid-flight.
//
// Only ever the document this job was writing, and only while it is still
// unpublished: a ready row under the same identifier is a previous good
// ingest, and the failed attempt must not take it with it.
func dropPartial(ctx context.Context, q *pgdbgen.Queries, id int64) error {
	docID, err := q.JobDocID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || !docID.Valid || docID.String == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the document of job %d: %w", id, err)
	}
	status, err := q.DocumentStatus(ctx, docID.String)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the status of %s: %w", docID.String, err)
	}
	if status == "ready" {
		return nil
	}
	return deleteRows(ctx, q, docID.String)
}

// inTx runs write inside one transaction that names this queue's user.
func (j *Jobs) inTx(ctx context.Context, write func(*pgdbgen.Queries) error) error {
	return session(ctx, j.db, j.q, j.userID, write)
}

// Add puts one source on the queue and returns its id and its position.
func (j *Jobs) Add(
	ctx context.Context,
	source, title string,
) (id int64, position int, err error) {
	// RETURNING, because Postgres has no last-insert-id.
	var ahead int64
	err = j.inTx(ctx, func(q *pgdbgen.Queries) error {
		var queueErr error
		id, queueErr = q.EnqueueJob(ctx, pgdbgen.EnqueueJobParams{
			UserID:     j.userID,
			SourcePath: source,
			Title:      nullString(title),
		})
		if queueErr != nil {
			return queueErr
		}
		// Counted in the same transaction as the insert, so the position is
		// the one this job actually took rather than one another worker's
		// claim has already moved.
		ahead, queueErr = q.QueuePosition(ctx, id)
		return queueErr
	})
	if err != nil {
		return 0, 0, fmt.Errorf("read the position of job %d: %w", id, err)
	}
	return id, int(ahead), nil
}

// List is the queue, with finished jobs where the caller asked for them.
func (j *Jobs) List(ctx context.Context, includeCompleted bool, limit int) ([]job.Job, error) {
	rows, err := j.rows(ctx, includeCompleted, limit)
	if err != nil {
		return nil, err
	}
	out := make([]job.Job, 0, len(rows))
	for _, row := range rows {
		out = append(out, jobOf(row))
	}
	return out, nil
}

// rows reads the queue, either the active jobs or the recent ones.
func (j *Jobs) rows(
	ctx context.Context,
	includeCompleted bool,
	limit int,
) ([]pgdbgen.IngestJob, error) {
	rows, err := read(ctx, j.db, j.q, j.userID,
		func(q *pgdbgen.Queries) ([]pgdbgen.IngestJob, error) {
			if includeCompleted {
				return q.RecentJobs(ctx, narrow(limit))
			}
			return q.ActiveJobs(ctx)
		})
	if err != nil {
		return nil, fmt.Errorf("read the queue: %w", err)
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// RequestCancel asks a job to stop and reports what it reads as now.
func (j *Jobs) RequestCancel(ctx context.Context, id int64) (string, error) {
	status, err := read(ctx, j.db, j.q, j.userID, func(q *pgdbgen.Queries) (string, error) {
		return q.JobStatus(ctx, id)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: job %d", ErrNotFound, id)
	}
	if err != nil {
		return "", fmt.Errorf("read job %d: %w", id, err)
	}
	// A job that already finished is not cancelled. Marking one would leave
	// a cancel request nothing ever reads.
	if status != "queued" && status != "running" {
		return status, nil
	}
	if err := j.inTx(ctx, func(q *pgdbgen.Queries) error {
		return q.RequestJobCancel(ctx, id)
	}); err != nil {
		return "", fmt.Errorf("cancel job %d: %w", id, err)
	}
	return "cancelling", nil
}

// jobOf is one queue row as the service reads it.
func jobOf(row pgdbgen.IngestJob) job.Job {
	j := job.Job{
		ProgressCurrent: intOf(row.ProgressCur),
		ProgressTotal:   intOf(row.ProgressTot),
		Source:          row.SourcePath,
		Title:           row.Title.String,
		DocID:           row.DocID.String,
		Status:          row.Status,
		Phase:           row.Phase.String,
		Error:           row.Error.String,
		CreatedAt:       row.CreatedAt.UTC().Format(timeFormat),
		UpdatedAt:       row.UpdatedAt.UTC().Format(timeFormat),
		JobID:           row.ID,
		Attempts:        int(row.Attempts),
		Permanent:       row.Permanent,
	}
	j.Quality, j.Warnings = summarize(row.Warnings)
	return j
}
