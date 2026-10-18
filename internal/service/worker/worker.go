// Package worker runs queued ingests, one at a time.
//
// It calls the same ingest service the CLI and the ConnectRPC API call. The
// difference is who drives it and who watches: a job row rather than a
// caller, and a status tool rather than a terminal.
//
// Crash recovery rests on the lease. A claimed job carries a lease, and the
// claim takes back any running job whose lease has expired, so a worker
// killed mid-job is recovered by the next one without anyone intervening.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bamsammich/docsearch/internal/libroot"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/urlguard"
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
	// DefaultJobTimeout is the longest one job may run. A crawl at its
	// fetch limit takes under an hour; this is for an extraction that has
	// wedged, which would otherwise hold the worker, and every account it
	// serves, indefinitely.
	DefaultJobTimeout = 4 * time.Hour
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
	// Force re-reads bytes the index already holds.
	Force bool
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
	// RenewLease extends a running job's lease. It fails with
	// ingest.ErrLostClaim once another worker has the job.
	RenewLease(ctx context.Context, id int64, lease time.Duration) error
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
	CancelPoll time.Duration
	// JobTimeout ends a job that runs longer, as a permanent failure.
	JobTimeout  time.Duration
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
	if opts.JobTimeout == 0 {
		opts.JobTimeout = DefaultJobTimeout
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
	worked, err := s.Next(ctx)
	if err != nil {
		return false, err
	}
	if !worked {
		if s.opts.Once {
			return true, nil
		}
		return sleep(ctx, s.opts.Poll) != nil, nil
	}
	return s.opts.Once, nil
}

// Next claims one job and runs it, and reports whether there was one. It
// never waits on an empty queue, which is what lets one worker take turns
// across many.
func (s *Service) Next(ctx context.Context) (bool, error) {
	job, err := s.jobs.Claim(ctx, s.opts.Lease, s.opts.MaxAttempts)
	if err != nil {
		return false, fmt.Errorf("claim a job: %w", err)
	}
	if job == nil {
		return false, nil
	}
	s.log.Info("claimed job",
		"job", job.ID, "attempt", job.Attempts, "source", job.Source)
	return true, s.execute(ctx, job)
}

// execute runs one job to a terminal state. An error means the queue itself
// could not be written to, which is not something the next job would
// survive either.
func (s *Service) execute(ctx context.Context, job *Job) error {
	source, err := s.sources.For(job.Source, true)
	if err != nil {
		// A target no source can be built from is refused the same way every
		// time, except a file inside a root that is not there yet: a volume
		// that mounts a minute late is worth the retry.
		return s.fail(ctx, job, err, !errors.Is(err, libroot.ErrMissing))
	}

	// Only the operator cancels a job. The worker shutting down waits for it
	// instead, which is what its stop grace period is for: a job cut off by
	// a deploy would otherwise be recorded as cancelled, or spend an attempt.
	jobCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	defer stop()
	watcher := s.watchForCancel(jobCtx, job.ID, stop)
	runCtx, expire := context.WithTimeoutCause(jobCtx, s.opts.JobTimeout, errTooLong)
	defer expire()

	result, err := s.ingester.Run(runCtx, source, ingest.Options{
		Title:      job.Title,
		JobID:      &job.ID,
		JobAttempt: job.Attempts,
		Force:      job.Force,
		Progress:   s.progress(jobCtx, job.ID),
		OnDocID:    s.recordDocID(jobCtx, job.ID),
	})
	stop()
	watched := <-watcher

	// A finished ingest is finished, however late a cancel request arrived:
	// its document is already published in the transaction that completed
	// the job. A job another worker took over belongs to that worker, and
	// this one writes nothing more to it.
	switch {
	case err == nil:
		return s.done(jobCtx, job, result)
	case watched == lost || errors.Is(err, ingest.ErrLostClaim):
		s.log.Warn("another worker took the job over; leaving it to them", "job", job.ID)
		return nil
	case errors.Is(context.Cause(runCtx), errTooLong):
		return s.fail(jobCtx, job, fmt.Errorf("%w (%s)", errTooLong, s.opts.JobTimeout), true)
	case watched == cancelled || errors.Is(err, ingest.ErrCancelled):
		return s.cancelled(jobCtx, job)
	default:
		return s.fail(jobCtx, job, err, permanent(err))
	}
}

// errTooLong is a job that ran past its timeout. Permanent, since what
// wedged once would wedge again on the next attempt.
var errTooLong = errors.New("the job ran past the longest a job may run")

// watchResult is how a job's watch ended.
type watchResult int

const (
	// running is a watch that ended with the ingest, nothing having happened.
	running watchResult = iota + 1
	// cancelled is the operator asking for the job to stop.
	cancelled
	// lost is the job claimed by another worker after this one's lease
	// lapsed.
	lost
)

// watchForCancel watches the job while it runs: it renews the lease, and it
// stops the ingest when the operator asks or when another worker has taken
// the job over. The channel carries which, once the watch ends.
//
// The lease is renewed here as well as by progress reports because a long
// extraction reports nothing until it is done, and a lease that lapsed
// mid-extraction would hand the job to a second worker. Polling rather
// than a trigger: the request arrives in another process, through a row,
// and a database this small has nothing to push with.
func (s *Service) watchForCancel(
	ctx context.Context,
	id int64,
	stop context.CancelFunc,
) <-chan watchResult {
	ended := make(chan watchResult, 1)
	go func() {
		defer close(ended)
		ended <- s.watch(ctx, id, stop)
	}()
	return ended
}

// watch polls until the ingest ends, the operator cancels, or the job is
// lost.
//
// A read that fails is not a cancellation: the operator has said nothing,
// and abandoning a running ingest over an unreadable row would throw away
// the work rather than protect it.
func (s *Service) watch(ctx context.Context, id int64, stop context.CancelFunc) watchResult {
	for {
		if err := sleep(ctx, s.opts.CancelPoll); err != nil {
			return running
		}
		// These run on a context the ingest's cancellation does not reach,
		// because reading the row is how a cancellation is noticed.
		if result, ended := s.check(context.WithoutCancel(ctx), id); ended {
			stop()
			return result
		}
	}
}

// check renews the lease and reads the cancel request, and reports whether
// the job has to stop and why.
func (s *Service) check(ctx context.Context, id int64) (watchResult, bool) {
	if err := s.jobs.RenewLease(ctx, id, s.opts.Lease); errors.Is(err, ingest.ErrLostClaim) {
		s.log.Warn("lost the job to another worker", "job", id)
		return lost, true
	} else if err != nil {
		s.log.Warn("could not renew the lease", "job", id, "error", err)
	}
	requested, err := s.jobs.CancelRequested(ctx, id)
	if err != nil {
		s.log.Warn("could not read the cancel request", "job", id, "error", err)
		return running, false
	}
	if requested {
		s.log.Info("operator cancelled the job", "job", id)
		return cancelled, true
	}
	return running, false
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
// A refused structure, an unreadable format and an address the guard
// refuses are all facts about the target, so the next attempt would meet the
// same refusal. Everything else is treated as the moment rather than the
// target, a host that did not resolve included.
func permanent(err error) bool {
	var structure *ingest.StructureError
	return errors.As(err, &structure) || errors.Is(err, ingest.ErrUnsupportedFormat) ||
		refused(err)
}

// refused is an address the guard refuses by policy, which it will refuse on
// every attempt. A host that did not resolve is worth another try.
func refused(err error) bool {
	return errors.Is(err, urlguard.ErrBlocked) && !errors.Is(err, urlguard.ErrUnresolved)
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
