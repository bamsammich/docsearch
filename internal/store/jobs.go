package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
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

const jobColumns = `id, source_path, title, doc_id, status, phase, progress_cur,
	progress_tot, attempts, permanent, error, warnings, lease_until, created_at, updated_at`

// Enqueue inserts a queued job and reports its position in the queue.
//
// It writes only to ingest_jobs, does no extraction, and never blocks.
func (s *Store) Enqueue(ctx context.Context, path, title string) (int64, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	var titleArg any
	if title != "" {
		titleArg = title
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO ingest_jobs (source_path, title, status, created_at, updated_at)
		VALUES (?, ?, 'queued', datetime('now'), datetime('now'))`, path, titleArg)
	if err != nil {
		return 0, 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	var position int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ingest_jobs WHERE status IN ('queued','running') AND id <= ?`,
		id).Scan(&position); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return id, position, nil
}

// JobByID returns one job.
func (s *Store) JobByID(ctx context.Context, id int64) (*Job, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+jobColumns+` FROM ingest_jobs WHERE id = ?`, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: job %d", ErrNotFound, id)
	}
	return job, err
}

// ActiveJobs returns queued and running jobs, plus recently finished ones when
// includeCompleted is set.
func (s *Store) ActiveJobs(ctx context.Context, includeCompleted bool, limit int) ([]Job, error) {
	q := `SELECT ` + jobColumns + ` FROM ingest_jobs WHERE status IN ('queued','running')
	       ORDER BY created_at, id`
	if includeCompleted {
		q = `SELECT ` + jobColumns + ` FROM ingest_jobs
		      ORDER BY (status IN ('queued','running')) DESC, updated_at DESC
		      LIMIT ?`
	}
	var rows *sql.Rows
	var err error
	if includeCompleted {
		rows, err = s.db.QueryContext(ctx, q, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *job)
	}
	return out, rows.Err()
}

// RequestCancel sets cancel_req on a queued or running job.
func (s *Store) RequestCancel(ctx context.Context, id int64) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM ingest_jobs WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: job %d", ErrNotFound, id)
	}
	if err != nil {
		return "", err
	}
	if status != "queued" && status != "running" {
		return status, nil
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE ingest_jobs SET cancel_req = 1, updated_at = datetime('now') WHERE id = ?`, id)
	return status, err
}

type scanner interface{ Scan(dest ...any) error }

func scanJob(row scanner) (*Job, error) {
	var (
		j          Job
		title      sql.NullString
		docID      sql.NullString
		phase      sql.NullString
		cur, tot   sql.NullInt64
		permanent  int
		errText    sql.NullString
		warnings   sql.NullString
		leaseUntil sql.NullString
	)
	if err := row.Scan(&j.JobID, &j.SourcePath, &title, &docID, &j.Status, &phase, &cur, &tot,
		&j.Attempts, &permanent, &errText, &warnings, &leaseUntil, &j.CreatedAt,
		&j.UpdatedAt); err != nil {
		return nil, err
	}
	j.Title = title.String
	j.DocID = docID.String
	j.Phase = phase.String
	j.Error = errText.String
	if cur.Valid {
		v := int(cur.Int64)
		j.ProgressCur = &v
	}
	if tot.Valid {
		v := int(tot.Int64)
		j.ProgressTot = &v
	}
	if cur.Valid && tot.Valid && tot.Int64 > 0 {
		j.Progress = fmt.Sprintf("%d/%d (%.0f%%)", cur.Int64, tot.Int64,
			100*float64(cur.Int64)/float64(tot.Int64))
	}
	j.Quality, j.Warnings = summarizeWarnings(warnings)
	if j.Quality == "unknown" {
		j.Quality = ""
	}

	// A failure that will recur identically reads differently from one that
	// ran out of attempts. Both end as status='failed'; only this tells the
	// reader whether retrying could ever have helped.
	if j.Status == "failed" {
		if permanent == 1 {
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
	if j.Status == "running" && leaseUntil.Valid {
		if lease, err := parseSQLiteTime(leaseUntil.String); err == nil {
			if time.Now().UTC().After(lease) {
				j.Stalled = true
				j.StalledNote = fmt.Sprintf(
					"the worker's lease expired at %s and has not been renewed; the worker "+
						"holding this job is probably dead. The job becomes reclaimable by "+
						"the next worker to poll, which will restart it.", leaseUntil.String)
			}
		}
	}
	return &j, nil
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
