package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/store/dbgen"
)

// Job is one row of the ingest queue as reported by ingest_status.
type Job struct {
	JobID       int64    `json:"job_id"`
	SourcePath  string   `json:"source_path"`
	Title       string   `json:"title,omitempty"`
	DocID       string   `json:"doc_id,omitempty"`
	Status      string   `json:"status"`
	Phase       string   `json:"phase,omitempty"`
	ProgressCur *int     `json:"progress_current,omitempty"`
	ProgressTot *int     `json:"progress_total,omitempty"`
	Progress    string   `json:"progress,omitempty"`
	Attempts    int      `json:"attempts"`
	Disposition string   `json:"disposition,omitempty"`
	Error       string   `json:"error,omitempty"`
	Quality     string   `json:"quality,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	Elapsed     string   `json:"elapsed"`
	Stalled     bool     `json:"stalled,omitempty"`
	StalledNote string   `json:"stalled_note,omitempty"`
}

// Enqueue inserts a queued job and reports its position in the queue.
//
// It writes only to ingest_jobs, does no extraction, and never blocks. The
// insert and the position count share a transaction so the position cannot
// count a job queued between them.
func (s *Store) Enqueue(ctx context.Context, path, title string) (int64, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	q := s.q.WithTx(tx)

	res, err := q.EnqueueJob(ctx, dbgen.EnqueueJobParams{
		SourcePath: path,
		Title:      sql.NullString{String: title, Valid: title != ""},
	})
	if err != nil {
		return 0, 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	position, err := q.QueuePosition(ctx, id)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return id, int(position), nil
}

// JobByID returns one job.
func (s *Store) JobByID(ctx context.Context, id int64) (*Job, error) {
	row, err := s.q.JobByID(ctx, id)
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
	var rows []dbgen.IngestJob
	var err error
	if includeCompleted {
		rows, err = s.q.RecentJobs(ctx, int64(limit))
	} else {
		rows, err = s.q.ActiveJobs(ctx)
	}
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
	status, err := s.q.JobStatus(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: job %d", ErrNotFound, id)
	}
	if err != nil {
		return "", err
	}
	if status != "queued" && status != "running" {
		return status, nil
	}
	return status, s.q.RequestJobCancel(ctx, id)
}

// mapJob turns a queue row into what ingest_status reports.
//
// Everything past the plain field copies is derived rather than stored:
// progress as a percentage, whether a failure can recur, how long the job has
// been running, and whether its worker is still alive.
func mapJob(r dbgen.IngestJob) Job {
	j := Job{
		JobID:      r.ID,
		SourcePath: r.SourcePath,
		Title:      r.Title.String,
		DocID:      r.DocID.String,
		Status:     r.Status,
		Phase:      r.Phase.String,
		Attempts:   int(r.Attempts),
		Error:      r.Error.String,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
	}
	cur, tot := r.ProgressCur, r.ProgressTot
	j.ProgressCur = nullInt(cur)
	j.ProgressTot = nullInt(tot)
	if cur.Valid && tot.Valid && tot.Int64 > 0 {
		j.Progress = fmt.Sprintf("%d/%d (%.0f%%)", cur.Int64, tot.Int64,
			100*float64(cur.Int64)/float64(tot.Int64))
	}
	j.Quality, j.Warnings = summarizeWarnings(r.Warnings)
	if j.Quality == "unknown" {
		j.Quality = ""
	}

	// A failure that will recur identically reads differently from one that
	// ran out of attempts. Both end as status='failed'; only this tells the
	// reader whether retrying could ever have helped.
	if j.Status == "failed" {
		if r.Permanent == 1 {
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
	if j.Status == "running" && r.LeaseUntil.Valid {
		if lease, err := parseSQLiteTime(r.LeaseUntil.String); err == nil {
			if time.Now().UTC().After(lease) {
				j.Stalled = true
				j.StalledNote = fmt.Sprintf(
					"the worker's lease expired at %s and has not been renewed; the worker "+
						"holding this job is probably dead. The job becomes reclaimable by "+
						"the next worker to poll, which will restart it.", r.LeaseUntil.String)
			}
		}
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
	if status != "queued" && status != "running" {
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
