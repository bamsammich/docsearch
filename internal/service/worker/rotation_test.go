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
	queue.EXPECT().Finish(mock.Anything, mock.Anything, mock.Anything, mock.Anything, true).
		Return(nil).Maybe()
	sources := mocks.NewMockSources(s.T())
	sources.EXPECT().For(mock.Anything, mock.Anything).Return(nil, errors.New("unreadable")).Maybe()
	return worker.New(queue, mocks.NewMockIngester(s.T()), sources,
		worker.Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// One library's long queue does not starve another: each gets a job per
// round, and a library with none is passed over.
func (s *RotationSuite) TestEachLibraryTakesATurn() {
	libraries := map[string]*worker.Service{
		"a": s.library("a", 1, 2, 3),
		"b": s.library("b", 4),
		"c": s.library("c"),
	}
	accounts := mocks.NewMockAccounts(s.T())
	accounts.EXPECT().Users(mock.Anything).Return([]string{"a", "b", "c"}, nil)

	err := worker.NewRotation(accounts, func(user string) *worker.Service {
		return libraries[user]
	}, time.Millisecond, true, slog.New(slog.DiscardHandler)).Run(s.T().Context())

	s.Require().NoError(err)
	s.Equal([]string{"a", "b", "a", "a"}, s.claimed)
}

// An account created while the worker runs is served from the next round.
func (s *RotationSuite) TestANewAccountIsServedWithoutARestart() {
	libraries := map[string]*worker.Service{
		"a":   s.library("a"),
		"new": s.library("new", 9),
	}
	accounts := mocks.NewMockAccounts(s.T())
	accounts.EXPECT().Users(mock.Anything).Return([]string{"a"}, nil).Once()
	accounts.EXPECT().Users(mock.Anything).Return([]string{"a", "new"}, nil)

	rotation := worker.NewRotation(accounts, func(user string) *worker.Service {
		return libraries[user]
	}, time.Millisecond, false, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(s.T().Context(), 200*time.Millisecond)
	defer cancel()
	s.Require().NoError(rotation.Run(ctx))
	s.Equal([]string{"new"}, s.claimed)
}
