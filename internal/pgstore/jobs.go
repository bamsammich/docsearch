package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
)

// Job is one row of the ingest queue as reported by ingest_status.
type Job struct {
	ProgressCur *int     `json:"progress_current,omitempty"`
	ProgressTot *int     `json:"progress_total,omitempty"`
	SourcePath  string   `json:"source_path"`
	Title       string   `json:"title,omitempty"`
	DocID       string   `json:"doc_id,omitempty"`
	Status      string   `json:"status"`
	Phase       string   `json:"phase,omitempty"`
	Progress    string   `json:"progress,omitempty"`
	Disposition string   `json:"disposition,omitempty"`
	Error       string   `json:"error,omitempty"`
	Quality     string   `json:"quality,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	Elapsed     string   `json:"elapsed"`
	StalledNote string   `json:"stalled_note,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	JobID       int64    `json:"job_id"`
	Attempts    int      `json:"attempts"`
	Stalled     bool     `json:"stalled,omitempty"`
}

// Enqueue inserts a queued job and reports its position in the queue.
//
// It writes only to ingest_jobs, does no extraction, and never blocks. The
// insert and the position count share a transaction so the position cannot
// count a job queued between them.
func (s *Store) Enqueue(ctx context.Context, path, title string) (int64, int, error) {
	var id, position int64
	err := pgsession.Run(ctx, s.db, s.q, s.userID, func(q *pgdbgen.Queries) error {
		// RETURNING, because Postgres has no last-insert-id.
		var queueErr error
		id, queueErr = q.EnqueueJob(ctx, pgdbgen.EnqueueJobParams{
			UserID:     s.userID,
			SourcePath: path,
			Title:      sql.NullString{String: title, Valid: title != ""},
		})
		if queueErr != nil {
			return queueErr
		}
		position, queueErr = q.QueuePosition(ctx, id)
		return queueErr
	})
	if err != nil {
		return 0, 0, err
	}
	return id, int(position), nil
}

// JobByID returns one job.
func (s *Store) JobByID(ctx context.Context, id int64) (*Job, error) {
	row, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) (pgdbgen.IngestJob, error) {
			return q.JobByID(ctx, id)
		})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: job %d", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	job := mapJob(row)
	return &job, nil
}

// ActiveJobs returns queued and running jobs, plus recently finished ones when
// includeCompleted is set.
func (s *Store) ActiveJobs(ctx context.Context, includeCompleted bool, limit int) ([]Job, error) {
	rows, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) ([]pgdbgen.IngestJob, error) {
			if includeCompleted {
				return q.RecentJobs(ctx, pgsession.Narrow(limit))
			}
			return q.ActiveJobs(ctx)
		})
	if err != nil {
		return nil, err
	}
	var out []Job
	for _, r := range rows {
		out = append(out, mapJob(r))
	}
	return out, nil
}

// RequestCancel sets cancel_req on a queued or running job.
func (s *Store) RequestCancel(ctx context.Context, id int64) (string, error) {
	status, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) (string, error) {
			return q.JobStatus(ctx, id)
		})
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: job %d", ErrNotFound, id)
	}
	if err != nil {
		return "", err
	}
	if status != statusQueued && status != statusRunning {
		return status, nil
	}
	return status, pgsession.Run(ctx, s.db, s.q, s.userID, func(q *pgdbgen.Queries) error {
		return q.RequestJobCancel(ctx, id)
	})
}

// mapJob turns a queue row into what ingest_status reports.
//
// Everything past the plain field copies is derived rather than stored:
// progress as a percentage, whether a failure can recur, how long the job has
// been running, and whether its worker is still alive.
func mapJob(r pgdbgen.IngestJob) Job {
	j := Job{
		JobID:      r.ID,
		SourcePath: r.SourcePath,
		Title:      r.Title.String,
		DocID:      r.DocID.String,
		Status:     r.Status,
		Phase:      r.Phase.String,
		Attempts:   int(r.Attempts),
		Error:      r.Error.String,
		CreatedAt:  r.CreatedAt.UTC().Format(timeFormat),
		UpdatedAt:  r.UpdatedAt.UTC().Format(timeFormat),
	}
	cur, tot := r.ProgressCur, r.ProgressTot
	j.ProgressCur = nullInt(cur)
	j.ProgressTot = nullInt(tot)
	if cur.Valid && tot.Valid && tot.Int32 > 0 {
		j.Progress = fmt.Sprintf("%d/%d (%.0f%%)", cur.Int32, tot.Int32,
			100*float64(cur.Int32)/float64(tot.Int32))
	}
	j.Quality, j.Warnings = summarizeWarnings(r.Warnings)
	if j.Quality == qualityUnknown {
		j.Quality = ""
	}

	// A failure that will recur identically reads differently from one that
	// ran out of attempts. Both end as status='failed'; only this tells the
	// reader whether retrying could ever have helped.
	if j.Status == "failed" {
		if r.Permanent {
			j.Disposition = fmt.Sprintf(
				"failed permanently after %d attempt(s); will not be retried, because this "+
					"failure would recur identically", j.Attempts)
		} else {
			j.Disposition = fmt.Sprintf(
				"exhausted after %d attempt(s); the failure was transient but did not "+
					"resolve", j.Attempts)
		}
	}

	j.Elapsed = elapsedSince(j.CreatedAt, j.UpdatedAt, j.Status)

	// A running job whose lease has lapsed is almost certainly orphaned. Say
	// so: reporting its last progress as if it were live is worse than useless
	// to someone waiting on it.
	// The lease is a timestamptz, so the comparison needs no parsing: what
	// the TEXT column made a string question is now a question about time.
	if j.Status == statusRunning && r.LeaseUntil.Valid &&
		time.Now().UTC().After(r.LeaseUntil.Time.UTC()) {
		j.Stalled = true
		j.StalledNote = fmt.Sprintf(
			"the worker's lease expired at %s and has not been renewed; the worker "+
				"holding this job is probably dead. The job becomes reclaimable by "+
				"the next worker to poll, which will restart it.",
			r.LeaseUntil.Time.UTC().Format(timeFormat))
	}
	return j
}

func parseSQLiteTime(v string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", v)
}

func elapsedSince(created, updated, status string) string {
	start, err := parseSQLiteTime(created)
	if err != nil {
		return ""
	}
	end := time.Now().UTC()
	if status != statusQueued && status != statusRunning {
		if u, err := parseSQLiteTime(updated); err == nil {
			end = u
		}
	}
	d := end.Sub(start).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}
