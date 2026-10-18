package mcpserver

import (
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/service/job"
)

// jobStatus is one row of the ingest queue as ingest_status reports it.
type jobStatus struct {
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

// The statuses a job reads as while it has not finished.
const (
	statusQueued  = "queued"
	statusRunning = "running"
	statusFailed  = "failed"
)

// statusOf is a job as ingest_status reports it at now.
//
// Everything past the plain field copies is derived rather than stored:
// progress as a percentage, whether a failure can recur, how long the job has
// been running, and whether its worker is still alive.
func statusOf(j job.Job, now time.Time) jobStatus {
	out := jobStatus{
		ProgressCur: j.ProgressCurrent,
		ProgressTot: j.ProgressTotal,
		SourcePath:  j.Source,
		Title:       j.Title,
		DocID:       j.DocID,
		Status:      j.Status,
		Phase:       j.Phase,
		Error:       j.Error,
		CreatedAt:   j.CreatedAt,
		UpdatedAt:   j.UpdatedAt,
		Warnings:    j.Warnings,
		JobID:       j.JobID,
		Attempts:    j.Attempts,
		Progress:    progressOf(j),
		Disposition: dispositionOf(j),
		Elapsed:     elapsed(j, now),
	}
	if j.Quality != 0 {
		out.Quality = j.Quality.String()
	}
	// A running job whose lease has lapsed is almost certainly orphaned. Say
	// so: reporting its last progress as if it were live is worse than
	// useless to someone waiting on it.
	if j.Status == statusRunning && !j.LeaseUntil.IsZero() && now.After(j.LeaseUntil) {
		out.Stalled = true
		out.StalledNote = fmt.Sprintf(
			"the worker's lease expired at %s and has not been renewed; the worker "+
				"holding this job is probably dead. The job becomes reclaimable by "+
				"the next worker to poll, which will restart it.",
			j.LeaseUntil.UTC().Format(time.RFC3339))
	}
	return out
}

// progressOf is how far a job has got, as a count and a percentage.
func progressOf(j job.Job) string {
	if j.ProgressCurrent == nil || j.ProgressTotal == nil || *j.ProgressTotal <= 0 {
		return ""
	}
	cur, tot := *j.ProgressCurrent, *j.ProgressTotal
	return fmt.Sprintf("%d/%d (%.0f%%)", cur, tot, 100*float64(cur)/float64(tot))
}

// dispositionOf says whether a failed job could ever have succeeded. Both
// kinds end as status 'failed'; only this tells the reader whether retrying
// could have helped.
func dispositionOf(j job.Job) string {
	switch {
	case j.Status != statusFailed:
		return ""
	case j.Permanent:
		return fmt.Sprintf(
			"failed permanently after %d attempt(s); will not be retried, because this "+
				"failure would recur identically", j.Attempts)
	default:
		return fmt.Sprintf(
			"exhausted after %d attempt(s); the failure was transient but did not "+
				"resolve", j.Attempts)
	}
}

// elapsed is how long a job has run, or ran: to now while it is unfinished,
// to its last update once it is done.
func elapsed(j job.Job, now time.Time) string {
	start, err := time.Parse(time.RFC3339, j.CreatedAt)
	if err != nil {
		return ""
	}
	end := now.UTC()
	if j.Status != statusQueued && j.Status != statusRunning {
		if updated, err := time.Parse(time.RFC3339, j.UpdatedAt); err == nil {
			end = updated
		}
	}
	return max(end.Sub(start).Round(time.Second), 0).String()
}
