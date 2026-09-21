package connectapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/api/connectapi"
	"github.com/bamsammich/docsearch/internal/api/connectapi/mocks"
	ingestv1 "github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1/ingestv1connect"
	typev1 "github.com/bamsammich/docsearch/internal/api/docsearch/type/v1"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/job"
)

// errNoJob stands in for the queue's sentinel.
var errNoJob = errors.New("no such job")

// JobAPISuite drives the real Connect stack over HTTP.
type JobAPISuite struct {
	suite.Suite
	jobs   *mocks.MockJobs
	client ingestv1connect.JobServiceClient
}

func TestJobAPI(t *testing.T) { suite.Run(t, new(JobAPISuite)) }

func (s *JobAPISuite) SetupTest() {
	s.jobs = mocks.NewMockJobs(s.T())

	path, handler := connectapi.NewJobServer(s.jobs, errNoJob)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	s.T().Cleanup(server.Close)

	s.client = ingestv1connect.NewJobServiceClient(server.Client(), server.URL)
}

func (s *JobAPISuite) TestQueueingReportsTheJobAndItsPosition() {
	s.jobs.EXPECT().Enqueue(mock.Anything, "/library/guide.md", "Guide").
		Return([]job.Queued{{Source: "/library/guide.md", JobID: 7, Position: 3}}, nil)

	res, err := s.client.Enqueue(s.T().Context(), connect.NewRequest(&ingestv1.EnqueueRequest{
		Source: "/library/guide.md", Title: "Guide",
	}))
	s.Require().NoError(err)
	s.Require().Len(res.Msg.GetJobs(), 1)
	s.Equal(int64(7), res.Msg.GetJobs()[0].GetJobId())
	s.Equal(int64(3), res.Msg.GetJobs()[0].GetQueuePosition())
}

func (s *JobAPISuite) TestADirectoryReportsEveryJobItQueued() {
	// A caller that queued a library has to be able to say what it got, so
	// each file beneath the directory comes back named.
	s.jobs.EXPECT().Enqueue(mock.Anything, "/library", "").Return([]job.Queued{
		{Source: "/library/a.md", JobID: 1, Position: 1},
		{Source: "/library/b.pdf", JobID: 2, Position: 2},
	}, nil)

	res, err := s.client.Enqueue(s.T().Context(),
		connect.NewRequest(&ingestv1.EnqueueRequest{Source: "/library"}))
	s.Require().NoError(err)
	s.Require().Len(res.Msg.GetJobs(), 2)
	s.Equal("/library/a.md", res.Msg.GetJobs()[0].GetSource())
	s.Equal(int64(2), res.Msg.GetJobs()[1].GetJobId())
	s.Equal(int64(2), res.Msg.GetJobs()[1].GetQueuePosition())
}

func (s *JobAPISuite) TestATargetNoAdapterReadsIsTheCallersMistake() {
	s.jobs.EXPECT().Enqueue(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, ingest.ErrUnsupportedFormat)

	_, err := s.client.Enqueue(s.T().Context(),
		connect.NewRequest(&ingestv1.EnqueueRequest{Source: "/library/archive.zip"}))
	s.Require().Error(err)
	s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))
}

func (s *JobAPISuite) TestAnEmptySourceIsRefusedBeforeAnythingIsAsked() {
	_, err := s.client.Enqueue(s.T().Context(),
		connect.NewRequest(&ingestv1.EnqueueRequest{}))
	s.Require().Error(err)
	s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))
}

func (s *JobAPISuite) TestAJobCarriesItsProgressAndItsGrade() {
	current, total := 120, 458
	s.jobs.EXPECT().List(mock.Anything, true, 10).Return([]job.Job{{
		ProgressCurrent: &current,
		ProgressTotal:   &total,
		Source:          "/library/manual.pdf",
		DocID:           "manual",
		Status:          "running",
		Phase:           "index",
		Warnings:        []string{"2 chunk(s) carry no heading path"},
		JobID:           7,
		Attempts:        1,
		Quality:         domain.QualityDegraded,
	}}, nil)

	res, err := s.client.ListJobs(s.T().Context(),
		connect.NewRequest(&ingestv1.ListJobsRequest{IncludeCompleted: true, Limit: 10}))
	s.Require().NoError(err)
	s.Require().Len(res.Msg.GetJobs(), 1)

	got := res.Msg.GetJobs()[0]
	s.Equal(int64(7), got.GetJobId())
	s.Equal("index", got.GetPhase())
	s.Equal(int64(120), got.GetProgressCurrent())
	s.Equal(int64(458), got.GetProgressTotal())
	s.Equal(typev1.Quality_QUALITY_DEGRADED, got.GetQuality())
	s.Equal([]string{"2 chunk(s) carry no heading path"}, got.GetWarnings())
}

func (s *JobAPISuite) TestAQueuedJobSendsNoProgress() {
	// A job that has not started has no progress, which is not progress of
	// zero.
	s.jobs.EXPECT().List(mock.Anything, false, 0).Return([]job.Job{{
		JobID: 7, Source: "/library/guide.md", Status: "queued",
	}}, nil)

	res, err := s.client.ListJobs(s.T().Context(),
		connect.NewRequest(&ingestv1.ListJobsRequest{}))
	s.Require().NoError(err)
	s.Nil(res.Msg.GetJobs()[0].ProgressCurrent)
}

func (s *JobAPISuite) TestAPermanentFailureSaysSo() {
	// What separates a job that failed deterministically from one that
	// exhausted its attempts.
	s.jobs.EXPECT().List(mock.Anything, true, 0).Return([]job.Job{{
		JobID: 7, Status: "failed", Attempts: 1,
		Permanent: true, Error: "produced no chunks",
	}}, nil)

	res, err := s.client.ListJobs(s.T().Context(),
		connect.NewRequest(&ingestv1.ListJobsRequest{IncludeCompleted: true}))
	s.Require().NoError(err)

	got := res.Msg.GetJobs()[0]
	s.True(got.GetPermanent())
	s.Equal(int64(1), got.GetAttempts())
	s.Contains(got.GetError(), "no chunks")
}

func (s *JobAPISuite) TestCancellingReportsWhatTheJobReadsAsNow() {
	s.jobs.EXPECT().Cancel(mock.Anything, int64(7)).Return("cancelling", nil)

	res, err := s.client.CancelJob(s.T().Context(),
		connect.NewRequest(&ingestv1.CancelJobRequest{JobId: 7}))
	s.Require().NoError(err)
	s.Equal("cancelling", res.Msg.GetStatus())
}

func (s *JobAPISuite) TestAJobTheQueueDoesNotHoldIsNotFound() {
	s.jobs.EXPECT().Cancel(mock.Anything, int64(404)).Return("", errNoJob)

	_, err := s.client.CancelJob(s.T().Context(),
		connect.NewRequest(&ingestv1.CancelJobRequest{JobId: 404}))
	s.Require().Error(err)
	s.Equal(connect.CodeNotFound, connect.CodeOf(err))
}
