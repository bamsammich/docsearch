package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/service/worker"
)

const (
	lease     = 5 * time.Minute
	builtinID = "default"
)

// JobsSuite runs the queue against a real database.
//
// What the claim is for is what happens when two workers race, and only a
// database shows that. Postgres changes the answer: SQLite allowed one
// writer, so a race was serialised by the write lock, while
// FOR UPDATE SKIP LOCKED lets two workers claim two different jobs at once.
type JobsSuite struct {
	suite.Suite
	db   *pgtest.DB
	jobs *postgres.Jobs
}

func TestJobs(t *testing.T) { suite.Run(t, new(JobsSuite)) }

func (s *JobsSuite) SetupTest() {
	s.db = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.db.Owner))
	s.jobs = postgres.NewJobs(s.db.App, builtinID)
}

// queued puts one source on the queue and returns its id.
func (s *JobsSuite) queued(source string) int64 {
	id, _, err := s.jobs.Add(s.T().Context(), source, "")
	s.Require().NoError(err)
	return id
}

func (s *JobsSuite) TestAQueuedJobIsClaimedOnce() {
	s.queued("/library/guide.md")

	claimed, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Require().NotNil(claimed)
	s.Equal("/library/guide.md", claimed.Source)
	s.Equal(1, claimed.Attempts)

	again, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Nil(again, "a claimed job is not on the queue")
}

func (s *JobsSuite) TestAnEmptyQueueIsNotAFailure() {
	claimed, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Nil(claimed)
}

func (s *JobsSuite) TestTwoWorkersRacingTakeTwoDifferentJobs() {
	// The property FOR UPDATE SKIP LOCKED buys. On SQLite one writer waited
	// for the other; here both proceed, and the second must pass over the
	// row the first locked rather than claim it or block on it.
	first := s.queued("/library/first.md")
	second := s.queued("/library/second.md")

	var wait sync.WaitGroup
	claims := make([]*worker.Job, 2)
	errs := make([]error, 2)
	for i := range claims {
		wait.Add(1)
		go func() {
			defer wait.Done()
			// A queue of its own per worker, as two processes would have.
			jobs := postgres.NewJobs(s.db.App, builtinID)
			claims[i], errs[i] = jobs.Claim(context.Background(), lease, 3)
		}()
	}
	wait.Wait()

	for i := range errs {
		s.Require().NoError(errs[i])
		s.Require().NotNil(claims[i], "both workers claimed something")
	}
	s.NotEqual(claims[0].ID, claims[1].ID, "two workers, two jobs")
	s.ElementsMatch([]int64{first, second}, []int64{claims[0].ID, claims[1].ID})
}

func (s *JobsSuite) TestAnExpiredLeaseIsReclaimed() {
	// Crash recovery: a worker killed mid-job leaves a running row whose
	// lease runs out, and the next worker takes it back without help.
	s.queued("/library/guide.md")
	claimed, err := s.jobs.Claim(s.T().Context(), -time.Second, 3)
	s.Require().NoError(err)
	s.Require().NotNil(claimed)

	reclaimed, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Require().NotNil(reclaimed)
	s.Equal(claimed.ID, reclaimed.ID)
	s.Equal(2, reclaimed.Attempts, "the attempt counter carries the retry")
}

func (s *JobsSuite) TestProgressRenewsTheLease() {
	// A job that is visibly advancing must never be reclaimed out from
	// under the worker running it.
	s.queued("/library/guide.md")
	claimed, err := s.jobs.Claim(s.T().Context(), -time.Second, 3)
	s.Require().NoError(err)
	s.Require().NotNil(claimed)

	s.Require().NoError(s.jobs.RecordProgress(
		s.T().Context(), claimed.ID, "index", 40, 100, lease))

	reclaimed, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Nil(reclaimed, "the renewed lease keeps the job")
}

func (s *JobsSuite) TestAJobAtItsAttemptCeilingIsNotClaimed() {
	// Excluded by the claim rather than after it: a job claimed and put
	// back forever would starve the queue.
	s.queued("/library/guide.md")
	for attempt := 1; attempt <= 3; attempt++ {
		claimed, err := s.jobs.Claim(s.T().Context(), -time.Second, 3)
		s.Require().NoError(err, attempt)
		s.Require().NotNil(claimed, attempt)
	}

	none, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Nil(none)
}

func (s *JobsSuite) TestACancelledJobIsNotClaimed() {
	id := s.queued("/library/guide.md")
	status, err := s.jobs.RequestCancel(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal("cancelling", status)

	claimed, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Nil(claimed)
}

func (s *JobsSuite) TestACancelRequestIsVisibleToTheWorkerRunningTheJob() {
	id := s.queued("/library/guide.md")
	claimed, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Require().NotNil(claimed)

	requested, err := s.jobs.CancelRequested(s.T().Context(), id)
	s.Require().NoError(err)
	s.False(requested)

	_, err = s.jobs.RequestCancel(s.T().Context(), id)
	s.Require().NoError(err)
	requested, err = s.jobs.CancelRequested(s.T().Context(), id)
	s.Require().NoError(err)
	s.True(requested)
}

func (s *JobsSuite) TestAFailedJobGoesBackOnTheQueueUntilItsAttemptsRunOut() {
	s.queued("/library/guide.md")
	claimed, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Require().NotNil(claimed)

	s.Require().NoError(s.jobs.Finish(
		s.T().Context(), claimed.ID, worker.Requeued, "a transient failure", false))

	again, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Require().NotNil(again, "a retryable failure is claimable again")
	s.Equal(claimed.ID, again.ID)
}

func (s *JobsSuite) TestAPermanentFailureStaysFailed() {
	s.queued("/library/archive.zip")
	claimed, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Require().NotNil(claimed)

	s.Require().NoError(s.jobs.Finish(
		s.T().Context(), claimed.ID, worker.Failed, "no adapter reads this", true))

	none, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Nil(none)

	jobs, err := s.jobs.List(s.T().Context(), true, 10)
	s.Require().NoError(err)
	s.Require().Len(jobs, 1)
	s.Equal("failed", jobs[0].Status)
	s.True(jobs[0].Permanent)
	s.Equal(1, jobs[0].Attempts, "the counter reads true rather than inflated")
}

func (s *JobsSuite) TestAPositionCountsTheJobItself() {
	_, first, err := s.jobs.Add(s.T().Context(), "/library/a.md", "")
	s.Require().NoError(err)
	_, second, err := s.jobs.Add(s.T().Context(), "/library/b.md", "")
	s.Require().NoError(err)

	s.Equal(1, first, "nothing ahead of it")
	s.Equal(2, second)
}

func (s *JobsSuite) TestTheQueueReadsNewestFirstWhenCompletedJobsAreIncluded() {
	s.queued("/library/a.md")
	id := s.queued("/library/b.md")
	claimed, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Require().NotNil(claimed)
	s.Require().NoError(s.jobs.CompleteWithoutDocument(s.T().Context(), claimed.ID, "a"))

	jobs, err := s.jobs.List(s.T().Context(), true, 10)
	s.Require().NoError(err)
	s.Require().Len(jobs, 2)
	s.Equal(id, jobs[0].JobID, "the still-queued job comes first")
}

func (s *JobsSuite) TestCancellingAJobTheQueueDoesNotHoldSaysSo() {
	status, err := s.jobs.RequestCancel(s.T().Context(), 404)
	s.Require().Error(err)
	s.Empty(status)
}
