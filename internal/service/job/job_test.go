package job_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/service/job"
	"github.com/bamsammich/docsearch/internal/service/job/mocks"
)

// JobSuite covers what queueing a target does, and what cancelling one
// reports.
type JobSuite struct {
	suite.Suite
	queue   *mocks.MockQueue
	targets *mocks.MockTargets
}

func TestJob(t *testing.T) { suite.Run(t, new(JobSuite)) }

func (s *JobSuite) SetupTest() {
	s.queue = mocks.NewMockQueue(s.T())
	s.targets = mocks.NewMockTargets(s.T())
}

func (s *JobSuite) service() *job.Service {
	return job.New(s.queue, s.targets)
}

func (s *JobSuite) TestAFileBecomesOneJob() {
	s.targets.EXPECT().Targets("/library/guide.md").
		Return([]string{"/library/guide.md"}, nil)
	s.queue.EXPECT().Add(mock.Anything, "/library/guide.md", "Guide").
		Return(7, 1, nil)

	queued, err := s.service().Enqueue(s.T().Context(), "/library/guide.md", "Guide")
	s.Require().NoError(err)
	s.Require().Len(queued, 1)
	s.Equal(int64(7), queued[0].JobID)
	s.Equal(1, queued[0].Position, "nothing ahead of it")
}

func (s *JobSuite) TestADirectoryBecomesOneJobPerFile() {
	// The site model applies to a crawled site, not to any directory that
	// happens to hold Markdown.
	s.targets.EXPECT().Targets("/library").Return([]string{
		"/library/a.md", "/library/b.pdf",
	}, nil)
	s.queue.EXPECT().Add(mock.Anything, "/library/a.md", "").Return(1, 1, nil)
	s.queue.EXPECT().Add(mock.Anything, "/library/b.pdf", "").Return(2, 2, nil)

	queued, err := s.service().Enqueue(s.T().Context(), "/library", "")
	s.Require().NoError(err)
	s.Len(queued, 2)
	s.Equal("/library/b.pdf", queued[1].Source)
}

func (s *JobSuite) TestATargetThatNamesNothingIsRefused() {
	refused := errors.New("no supported files under /library/empty")
	s.targets.EXPECT().Targets("/library/empty").Return(nil, refused)

	_, err := s.service().Enqueue(s.T().Context(), "/library/empty", "")
	s.Require().ErrorIs(err, refused)
}

func (s *JobSuite) TestAListingWithNoBoundTakesADefault() {
	s.queue.EXPECT().List(mock.Anything, false, 50).Return(nil, nil)

	_, err := s.service().List(s.T().Context(), false, 0)
	s.Require().NoError(err)
}

func (s *JobSuite) TestAListingKeepsTheBoundItWasGiven() {
	s.queue.EXPECT().List(mock.Anything, true, 5).Return([]job.Job{{JobID: 1}}, nil)

	jobs, err := s.service().List(s.T().Context(), true, 5)
	s.Require().NoError(err)
	s.Len(jobs, 1)
}

func (s *JobSuite) TestCancellingReportsWhatTheJobReadsAsNow() {
	// A job that already finished is not cancelled, and saying so beats
	// reporting a cancellation that did nothing.
	s.queue.EXPECT().RequestCancel(mock.Anything, int64(7)).Return("done", nil)

	status, err := s.service().Cancel(s.T().Context(), 7)
	s.Require().NoError(err)
	s.Equal("done", status)
}
