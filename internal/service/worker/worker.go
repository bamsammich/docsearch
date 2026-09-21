// Package worker runs queued ingests, one at a time.
//
// It calls the same ingest service the CLI and the ConnectRPC API call. The
// difference is who drives it and who watches: a job row rather than a
// caller, and a status tool rather than a terminal.
//
// Crash recovery rests on the lease. A claimed job carries a lease, and the
// claim takes back any running job whose lease has expired, so a worker
// killed mid-job is recovered by the next one without anyone intervening.
//
// Ported from python/docsearch/worker.py.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bamsammich/docsearch/internal/service/ingest"
)

const (
	// DefaultLease is how long a claim holds a job before another worker may
	// take it back.
	DefaultLease = 5 * time.Minute
	// DefaultPoll is the wait between looks at an empty queue.
	DefaultPoll = 2 * time.Second
	// DefaultMaxAttempts is how many times a job is tried before it is left
	// failed.
	DefaultMaxAttempts = 3
	// DefaultCancelPoll is the gap between asking whether the operator has
	// cancelled the running job.
	DefaultCancelPoll = 2 * time.Second
	// progressInterval is the shortest gap between progress writes.
	//
	// Progress must be frequent enough that a long extraction is visibly
	// advancing, since a status tool reporting only "running" for forty
	// minutes is indistinguishable from a hang. Each write also renews the
	// lease, so it need not be more often than this.
	progressInterval = 2 * time.Second
)

// Job is one row of the queue, as claimed.
type Job struct {
	// Title is what the operator called the document, and is empty where
	// they left it to name itself.
	Title string
	// Source is the path or URL to read.
	Source   string
	ID       int64
	Attempts int
}

// Outcome is how a job ended, as the queue records it.
type Outcome uint8

const (
	// Requeued is a job that failed in a way another attempt might survive,
	// and has attempts left.
	Requeued Outcome = iota + 1
	// Failed is a job that will not be tried again.
	Failed
)

// Jobs is the queue. The port is declared here because this package is what
// drives it.
type Jobs interface {
	// Claim takes the next job, or returns nil where the queue holds none.
	Claim(ctx context.Context, lease time.Duration, maxAttempts int) (*Job, error)
	// RecordProgress writes how far a job has got and renews its lease.
	RecordProgress(
		ctx context.Context,
		id int64,
		phase string,
		cur, tot int,
		lease time.Duration,
	) error
	// RecordDocID notes which document a running job is writing.
	RecordDocID(ctx context.Context, id int64, docID string) error
	// CancelRequested reports whether the operator has asked for the job to
	// stop.
	CancelRequested(ctx context.Context, id int64) (bool, error)
	// Cancel marks a job cancelled, having removed whatever it half wrote.
	Cancel(ctx context.Context, id int64) error
	// Finish marks a job failed or puts it back on the queue, having removed
	// whatever it half wrote.
	Finish(ctx context.Context, id int64, outcome Outcome, reason string, permanent bool) error
	// CompleteWithoutDocument marks a job done where its ingest wrote
	// nothing, which an unchanged source produces.
	CompleteWithoutDocument(ctx context.Context, id int64, docID string) error
}

// Ingester runs one ingest.
type Ingester interface {
	Run(ctx context.Context, src ingest.Source, opts ingest.Options) (*ingest.Result, error)
}

// Sources turns a job's target into something that can be read.
type Sources interface {
	For(target string, revalidate bool) (ingest.Source, error)
}

// Options bound one worker.
type Options struct {
	Log *slog.Logger
	// Lease, Poll, CancelPoll and MaxAttempts take their defaults when zero.
	Lease time.Duration
	Poll  time.Duration
	// CancelPoll is how often a running job is asked whether the operator
	// has cancelled it.
	CancelPoll  time.Duration
	MaxAttempts int
	// Once returns after one job, or after finding the queue empty. It is
	// what a test and a one-shot container want.
	Once bool
}

// Service is a worker.
type Service struct {
	jobs     Jobs
	ingester Ingester
	sources  Sources
	log      *slog.Logger
	opts     Options
}

// New builds a worker over the queue, the ingest service and the source
// registry.
func New(jobs Jobs, ingester Ingester, sources Sources, opts Options) *Service {
	if opts.Lease == 0 {
		opts.Lease = DefaultLease
	}
	if opts.Poll == 0 {
		opts.Poll = DefaultPoll
	}
	if opts.CancelPoll == 0 {
		opts.CancelPoll = DefaultCancelPoll
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = DefaultMaxAttempts
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{jobs: jobs, ingester: ingester, sources: sources, log: log, opts: opts}
}

// Run works the queue until the context is cancelled.
//
// Cancelling stops the worker between jobs rather than during one: a job
// interrupted part way leaves its lease to expire and is recovered by
// whoever claims it next, which is worse than letting it finish.
func (s *Service) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		stop, err := s.step(ctx)
		if err != nil || stop {
			return err
		}
	}
	return nil
}

// step claims one job and runs it, and reports whether the worker is done.
func (s *Service) step(ctx context.Context) (bool, error) {
	job, err := s.jobs.Claim(ctx, s.opts.Lease, s.opts.MaxAttempts)
	if err != nil {
		return false, fmt.Errorf("claim a job: %w", err)
	}
	if job == nil {
		if s.opts.Once {
			return true, nil
		}
		return sleep(ctx, s.opts.Poll) != nil, nil
	}
	s.log.Info("claimed job",
		"job", job.ID, "attempt", job.Attempts, "source", job.Source)
	if err := s.execute(ctx, job); err != nil {
		return false, err
	}
	return s.opts.Once, nil
}

// execute runs one job to a terminal state. An error means the queue itself
// could not be written to, which is not something the next job would
// survive either.
func (s *Service) execute(ctx context.Context, job *Job) error {
	source, err := s.sources.For(job.Source, true)
	if err != nil {
		// A target no source can be built from is refused the same way every
		// time, so trying again would read the same job row and fail again.
		return s.fail(ctx, job, err, true)
	}

	// The ingest is cancelled from two directions: the worker shutting down,
	// and the operator cancelling this job.
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	watcher := s.watchForCancel(ctx, job.ID, stop)

	result, err := s.ingester.Run(ctx, source, ingest.Options{
		Title:    job.Title,
		JobID:    &job.ID,
		Progress: s.progress(ctx, job.ID),
		OnDocID:  s.recordDocID(ctx, job.ID),
	})
	stop()
	cancelledByOperator := <-watcher

	switch {
	case cancelledByOperator || errors.Is(err, ingest.ErrCancelled):
		return s.cancelled(ctx, job)
	case err != nil:
		return s.fail(ctx, job, err, permanent(err))
	}
	return s.done(ctx, job, result)
}

// watchForCancel polls the job row and stops the ingest when the operator
// asks. The channel carries whether they did, once the watch ends.
//
// Polling rather than a trigger: the request arrives in another process,
// through a row, and a database this small has nothing to push with.
func (s *Service) watchForCancel(
	ctx context.Context,
	id int64,
	stop context.CancelFunc,
) <-chan bool {
	asked := make(chan bool, 1)
	go func() {
		defer close(asked)
		asked <- s.awaitCancel(ctx, id, stop)
	}()
	return asked
}

// awaitCancel polls until the operator cancels the job or the ingest ends,
// and reports which happened.
//
// A read that fails is not a cancellation: the operator has said nothing,
// and abandoning a running ingest over an unreadable row would throw away
// the work rather than protect it.
func (s *Service) awaitCancel(ctx context.Context, id int64, stop context.CancelFunc) bool {
	for {
		if err := sleep(ctx, s.opts.CancelPoll); err != nil {
			return false
		}
		// The read runs on a context the ingest's cancellation does not
		// reach, because reading the row is how a cancellation is noticed.
		requested, err := s.jobs.CancelRequested(context.WithoutCancel(ctx), id)
		if err != nil {
			s.log.Warn("could not read the cancel request", "job", id, "error", err)
			continue
		}
		if requested {
			s.log.Info("operator cancelled the job", "job", id)
			stop()
			return true
		}
	}
}

// progress writes how far the job has got, no more often than
// progressInterval, and always for the last report of a phase.
func (s *Service) progress(ctx context.Context, id int64) ingest.Progress {
	var last time.Time
	return func(phase ingest.Phase, cur, tot int) {
		now := time.Now()
		if now.Sub(last) < progressInterval && cur < tot {
			return
		}
		last = now
		err := s.jobs.RecordProgress(
			context.WithoutCancel(ctx), id, phase.String(), cur, tot, s.opts.Lease)
		if err != nil {
			// The ingest is the work; a progress row that could not be
			// written is not worth abandoning it for. The lease will expire
			// if the failure persists, and another worker will take over.
			s.log.Warn("could not write progress", "job", id, "error", err)
		}
	}
}

// recordDocID notes the identifier while the ingest runs, so a job that dies
// mid-write still says which document it was writing.
func (s *Service) recordDocID(ctx context.Context, id int64) func(string) {
	return func(docID string) {
		if err := s.jobs.RecordDocID(context.WithoutCancel(ctx), id, docID); err != nil {
			s.log.Warn("could not record the document", "job", id, "error", err)
		}
	}
}

// done records a finished ingest.
func (s *Service) done(ctx context.Context, job *Job, result *ingest.Result) error {
	if result.Outcome == ingest.Unchanged {
		// The ingest's final transaction never ran, because nothing was
		// re-indexed, so the job is completed on its own.
		err := s.jobs.CompleteWithoutDocument(context.WithoutCancel(ctx), job.ID, result.DocID)
		if err != nil {
			return fmt.Errorf("complete job %d: %w", job.ID, err)
		}
	}
	s.log.Info("job finished",
		"job", job.ID,
		"outcome", result.Outcome.String(),
		"document", result.DocID,
		"chunks", result.ChunkCount,
		"quality", quality(result))
	return nil
}

// cancelled records a job the operator stopped.
func (s *Service) cancelled(ctx context.Context, job *Job) error {
	if err := s.jobs.Cancel(context.WithoutCancel(ctx), job.ID); err != nil {
		return fmt.Errorf("cancel job %d: %w", job.ID, err)
	}
	s.log.Info("job cancelled; partial rows removed", "job", job.ID)
	return nil
}

// fail records a job that did not finish, putting it back on the queue where
// another attempt might survive and attempts remain.
func (s *Service) fail(ctx context.Context, job *Job, cause error, permanent bool) error {
	exhausted := permanent || job.Attempts >= s.opts.MaxAttempts
	outcome := Requeued
	if exhausted {
		outcome = Failed
	}
	err := s.jobs.Finish(
		context.WithoutCancel(ctx), job.ID, outcome, cause.Error(), permanent)
	if err != nil {
		return fmt.Errorf("fail job %d: %w", job.ID, err)
	}
	s.log.Warn("job failed",
		"job", job.ID,
		"permanent", permanent,
		"attempt", job.Attempts,
		"error", cause)
	return nil
}

// permanent reports whether a failure would recur on every attempt.
//
// A refused structure and an unreadable format are both facts about the
// document, so the next attempt would read the same bytes and refuse them
// again. Everything else is treated as the moment rather than the document.
func permanent(err error) bool {
	var structure *ingest.StructureError
	return errors.As(err, &structure) || errors.Is(err, ingest.ErrUnsupportedFormat)
}

// quality is the grade a finished ingest earned, for the log.
func quality(result *ingest.Result) string {
	if result.Report == nil {
		return "unknown"
	}
	return result.Report.Quality().String()
}

// sleep waits, and reports a cancelled context rather than waiting through
// it.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
