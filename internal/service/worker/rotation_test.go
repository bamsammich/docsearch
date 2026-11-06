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

	"github.com/bamsammich/docsearch/internal/service/worker"
	"github.com/bamsammich/docsearch/internal/service/worker/mocks"
)

// RotationSuite checks the order a rotation takes jobs in, with each job
// failing at once so that order is all a case observes.
type RotationSuite struct {
	suite.Suite
	claimed []string
}

func TestRotation(t *testing.T) { suite.Run(t, new(RotationSuite)) }

func (s *RotationSuite) SetupTest() { s.claimed = nil }

// queue names the jobs user queued in their own library.
func queue(user string) worker.Queue { return worker.Queue{User: user, Library: user} }

// library is a worker over a queue holding jobs, in order, for user.
func (s *RotationSuite) library(user string, jobs ...int64) *worker.Service {
	queue := mocks.NewMockJobs(s.T())
	for _, id := range jobs {
		queue.EXPECT().Claim(mock.Anything, mock.Anything, mock.Anything).
			RunAndReturn(func(context.Context, time.Duration, int) (*worker.Job, error) {
				s.claimed = append(s.claimed, user)
				return &worker.Job{ID: id, Source: "/library/x.md", Attempts: 1}, nil
			}).Once()
	}
	queue.EXPECT().Claim(mock.Anything, mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	queue.EXPECT().RenewLease(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	queue.EXPECT().Finish(mock.Anything, mock.Anything, mock.Anything, mock.Anything, true).
		Return(nil).Maybe()
	sources := mocks.NewMockSources(s.T())
	sources.EXPECT().For(mock.Anything, mock.Anything).Return(nil, errors.New("unreadable")).Maybe()
	return worker.New(queue, mocks.NewMockIngester(s.T()), sources,
		worker.Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// One queue's long backlog does not starve another: each gets a job per
// round, and a queue with none is passed over.
func (s *RotationSuite) TestEachQueueTakesATurn() {
	libraries := map[worker.Queue]*worker.Service{
		queue("a"): s.library("a", 1, 2, 3),
		queue("b"): s.library("b", 4),
		queue("c"): s.library("c"),
	}
	queues := mocks.NewMockQueues(s.T())
	queues.EXPECT().
		Waiting(mock.Anything).
		Return([]worker.Queue{queue("a"), queue("b"), queue("c")}, nil)

	err := worker.NewRotation(queues, func(q worker.Queue) *worker.Service {
		return libraries[q]
	}, time.Millisecond, true, slog.New(slog.DiscardHandler)).Run(s.T().Context())

	s.Require().NoError(err)
	s.Equal([]string{"a", "b", "a", "a"}, s.claimed)
}

// A queue that appears while the worker runs is served from the next round.
func (s *RotationSuite) TestANewQueueIsServedWithoutARestart() {
	libraries := map[worker.Queue]*worker.Service{
		queue("a"):   s.library("a"),
		queue("new"): s.library("new", 9),
	}
	queues := mocks.NewMockQueues(s.T())
	queues.EXPECT().Waiting(mock.Anything).Return([]worker.Queue{queue("a")}, nil).Once()
	queues.EXPECT().Waiting(mock.Anything).Return([]worker.Queue{queue("a"), queue("new")}, nil)

	rotation := worker.NewRotation(queues, func(q worker.Queue) *worker.Service {
		return libraries[q]
	}, time.Millisecond, false, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(s.T().Context(), 200*time.Millisecond)
	defer cancel()
	s.Require().NoError(rotation.Run(ctx))
	s.Equal([]string{"new"}, s.claimed)
}

// One queue failing does not stop the others: a requester who lost the
// grant mid-job leaves that queue unable to finish, and every other
// library's work goes on.
func (s *RotationSuite) TestAFailingQueueDoesNotStopTheOthers() {
	broken := mocks.NewMockJobs(s.T())
	broken.EXPECT().Claim(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("another worker has taken this job over"))
	libraries := map[worker.Queue]*worker.Service{
		queue("gone"): worker.New(broken, mocks.NewMockIngester(s.T()), mocks.NewMockSources(s.T()),
			worker.Options{Log: slog.New(slog.DiscardHandler)}),
		queue("b"): s.library("b", 4),
	}
	queues := mocks.NewMockQueues(s.T())
	queues.EXPECT().Waiting(mock.Anything).Return([]worker.Queue{queue("gone"), queue("b")}, nil)

	err := worker.NewRotation(queues, func(q worker.Queue) *worker.Service {
		return libraries[q]
	}, time.Millisecond, true, slog.New(slog.DiscardHandler)).Run(s.T().Context())

	s.Require().NoError(err)
	s.Equal([]string{"b"}, s.claimed)
}

// Every queue failing at once is the worker's own trouble, and stops it.
func (s *RotationSuite) TestEveryQueueFailingStopsTheWorker() {
	broken := mocks.NewMockJobs(s.T())
	broken.EXPECT().Claim(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("connection refused"))
	queues := mocks.NewMockQueues(s.T())
	queues.EXPECT().Waiting(mock.Anything).Return([]worker.Queue{queue("a")}, nil)

	err := worker.NewRotation(queues, func(worker.Queue) *worker.Service {
		return worker.New(broken, mocks.NewMockIngester(s.T()), mocks.NewMockSources(s.T()),
			worker.Options{Log: slog.New(slog.DiscardHandler)})
	}, time.Millisecond, true, slog.New(slog.DiscardHandler)).Run(s.T().Context())

	s.Require().ErrorContains(err, "connection refused")
}
