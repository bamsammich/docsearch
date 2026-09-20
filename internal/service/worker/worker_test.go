package worker_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/worker"
	"github.com/bamsammich/docsearch/internal/service/worker/mocks"
)

// WorkerSuite covers what one job's run records, and what it records when
// the ingest does not finish.
type WorkerSuite struct {
	suite.Suite
	jobs     *mocks.MockJobs
	ingester *mocks.MockIngester
	sources  *mocks.MockSources
}

func TestWorker(t *testing.T) { suite.Run(t, new(WorkerSuite)) }

func (s *WorkerSuite) SetupTest() {
	s.jobs = mocks.NewMockJobs(s.T())
	s.ingester = mocks.NewMockIngester(s.T())
	s.sources = mocks.NewMockSources(s.T())
}

// run works one job and returns.
func (s *WorkerSuite) run(opts worker.Options) error {
	opts.Once = true
	opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	if opts.CancelPoll == 0 {
		// The default asks every two seconds, which a suite should not wait
		// for. The interval is what a test varies; whether cancelling works
		// is not.
		opts.CancelPoll = time.Millisecond
	}
	return worker.New(s.jobs, s.ingester, s.sources, opts).Run(s.T().Context())
}

// claims is one job waiting on the queue.
func (s *WorkerSuite) claims(job *worker.Job) {
	s.jobs.EXPECT().
		Claim(mock.Anything, mock.Anything, mock.Anything).
		Return(job, nil).
		Once()
}

// buildsASource is a target the registry can read.
func (s *WorkerSuite) buildsASource() {
	s.sources.EXPECT().For(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
}

// neverCancelled is an operator who leaves the job alone.
func (s *WorkerSuite) neverCancelled() {
	s.jobs.EXPECT().CancelRequested(mock.Anything, mock.Anything).Return(false, nil).Maybe()
}

func (s *WorkerSuite) TestAFinishedIngestLeavesTheJobToItsOwnTransaction() {
	// The ingest completes the job in the transaction that makes the document
	// visible, so the worker must not complete it a second time.
	s.claims(&worker.Job{ID: 7, Source: "/library/guide.md", Attempts: 1})
	s.buildsASource()
	s.neverCancelled()
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		Return(&ingest.Result{
			DocID: "operator-guide", ChunkCount: 18, Outcome: ingest.Ingested,
		}, nil)

	s.Require().NoError(s.run(worker.Options{}))
}

func (s *WorkerSuite) TestTheJobCarriesTheIdentifierAndTitleIntoTheIngest() {
	s.claims(&worker.Job{ID: 7, Source: "/library/guide.md", Title: "Operator Guide"})
	s.buildsASource()
	s.neverCancelled()

	var seen ingest.Options
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(
			_ context.Context, _ ingest.Source, opts ingest.Options,
		) (*ingest.Result, error) {
			seen = opts
			return &ingest.Result{DocID: "guide", Outcome: ingest.Ingested}, nil
		})

	s.Require().NoError(s.run(worker.Options{}))
	s.Equal("Operator Guide", seen.Title)
	s.Require().NotNil(seen.JobID)
	s.Equal(int64(7), *seen.JobID)
}

func (s *WorkerSuite) TestAnUnchangedSourceCompletesTheJobOnItsOwn() {
	// Nothing was re-indexed, so the ingest's final transaction never ran and
	// the job would otherwise read as running forever.
	s.claims(&worker.Job{ID: 7, Source: "/library/copy.md"})
	s.buildsASource()
	s.neverCancelled()
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		Return(&ingest.Result{DocID: "guide", Outcome: ingest.Unchanged}, nil)
	s.jobs.EXPECT().CompleteWithoutDocument(mock.Anything, int64(7), "guide").Return(nil)

	s.Require().NoError(s.run(worker.Options{}))
}

func (s *WorkerSuite) TestARefusedStructureIsNotTriedAgain() {
	// The next attempt would read the same bytes and refuse them again.
	s.claims(&worker.Job{ID: 7, Source: "/library/empty.md", Attempts: 1})
	s.buildsASource()
	s.neverCancelled()
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &ingest.StructureError{Message: "produced no chunks"})

	var permanent bool
	var outcome worker.Outcome
	s.jobs.EXPECT().Finish(mock.Anything, int64(7), mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(
			_ context.Context, _ int64, o worker.Outcome, _ string, p bool,
		) error {
			outcome, permanent = o, p
			return nil
		})

	s.Require().NoError(s.run(worker.Options{}))
	s.True(permanent)
	s.Equal(worker.Failed, outcome, "not requeued, though attempts remain")
}

func (s *WorkerSuite) TestATransientFailureGoesBackOnTheQueue() {
	s.claims(&worker.Job{ID: 7, Source: "/library/guide.md", Attempts: 1})
	s.buildsASource()
	s.neverCancelled()
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("database is locked"))

	var outcome worker.Outcome
	var reason string
	s.jobs.EXPECT().Finish(mock.Anything, int64(7), mock.Anything, mock.Anything, false).
		RunAndReturn(func(
			_ context.Context, _ int64, o worker.Outcome, r string, _ bool,
		) error {
			outcome, reason = o, r
			return nil
		})

	s.Require().NoError(s.run(worker.Options{}))
	s.Equal(worker.Requeued, outcome)
	s.Contains(reason, "database is locked")
}

func (s *WorkerSuite) TestTheLastAttemptLeavesTheJobFailed() {
	s.claims(&worker.Job{ID: 7, Source: "/library/guide.md", Attempts: 3})
	s.buildsASource()
	s.neverCancelled()
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("database is locked"))

	var outcome worker.Outcome
	s.jobs.EXPECT().Finish(mock.Anything, int64(7), mock.Anything, mock.Anything, false).
		RunAndReturn(func(
			_ context.Context, _ int64, o worker.Outcome, _ string, _ bool,
		) error {
			outcome = o
			return nil
		})

	s.Require().NoError(s.run(worker.Options{MaxAttempts: 3}))
	s.Equal(worker.Failed, outcome, "transient, but out of attempts")
}

func (s *WorkerSuite) TestATargetNoSourceCanBeBuiltFromNeverRuns() {
	s.claims(&worker.Job{ID: 7, Source: "/etc/passwd"})
	s.sources.EXPECT().For("/etc/passwd", true).
		Return(nil, errors.New("path is not inside a configured library root"))
	s.jobs.EXPECT().Finish(mock.Anything, int64(7), worker.Failed, mock.Anything, true).
		Return(nil)

	s.Require().NoError(s.run(worker.Options{}))
}

func (s *WorkerSuite) TestACancelRequestStopsTheIngestAndClearsWhatItWrote() {
	s.claims(&worker.Job{ID: 7, Source: "/library/manual.pdf"})
	s.buildsASource()
	s.jobs.EXPECT().CancelRequested(mock.Anything, int64(7)).Return(true, nil)
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(
			ctx context.Context, _ ingest.Source, _ ingest.Options,
		) (*ingest.Result, error) {
			<-ctx.Done()
			return nil, ingest.ErrCancelled
		})
	s.jobs.EXPECT().Cancel(mock.Anything, int64(7)).Return(nil)

	s.Require().NoError(s.run(worker.Options{}))
}

func (s *WorkerSuite) TestAnEmptyQueueIsNotAnError() {
	s.jobs.EXPECT().Claim(mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)
	s.Require().NoError(s.run(worker.Options{}))
}

func (s *WorkerSuite) TestAQueueThatCannotBeReadStopsTheWorker() {
	// A worker that cannot reach its queue has nothing to do but exit, and
	// exiting is what a supervisor restarts.
	s.jobs.EXPECT().Claim(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("database is locked"))

	err := s.run(worker.Options{})
	s.Require().ErrorContains(err, "claim a job")
}

func (s *WorkerSuite) TestProgressIsThrottledButThePhasesLastReportAlwaysLands() {
	// A status tool showing only "running" for forty minutes is
	// indistinguishable from a hang, and each write renews the lease.
	s.claims(&worker.Job{ID: 7, Source: "https://example.com/docs/"})
	s.buildsASource()
	s.neverCancelled()

	var phases []string
	s.jobs.EXPECT().
		RecordProgress(mock.Anything, int64(7), mock.Anything,
			mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(
			_ context.Context, _ int64, phase string, _, _ int, _ time.Duration,
		) error {
			phases = append(phases, phase)
			return nil
		}).Maybe()

	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(
			_ context.Context, _ ingest.Source, opts ingest.Options,
		) (*ingest.Result, error) {
			// Three fetch reports in quick succession: the first lands, the
			// middle one is throttled away, and the last lands because it
			// completes the phase.
			opts.Progress(ingest.PhaseFetch, 1, 10)
			opts.Progress(ingest.PhaseFetch, 2, 10)
			opts.Progress(ingest.PhaseFetch, 10, 10)
			return &ingest.Result{DocID: "docs", Outcome: ingest.Ingested}, nil
		})

	s.Require().NoError(s.run(worker.Options{}))
	s.Equal([]string{"fetch", "fetch"}, phases)
}

func (s *WorkerSuite) TestTheDocumentIsRecordedWhileTheIngestRuns() {
	// A job that dies mid-write still says which document it was writing,
	// which is what lets the next worker clean up after it.
	s.claims(&worker.Job{ID: 7, Source: "/library/guide.md"})
	s.buildsASource()
	s.neverCancelled()
	s.jobs.EXPECT().RecordDocID(mock.Anything, int64(7), "operator-guide").Return(nil)
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(
			_ context.Context, _ ingest.Source, opts ingest.Options,
		) (*ingest.Result, error) {
			opts.OnDocID("operator-guide")
			return &ingest.Result{DocID: "operator-guide", Outcome: ingest.Ingested}, nil
		})

	s.Require().NoError(s.run(worker.Options{}))
}
