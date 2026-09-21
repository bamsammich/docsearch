package connectapi

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	ingestv1 "github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1/ingestv1connect"
	typev1 "github.com/bamsammich/docsearch/internal/api/docsearch/type/v1"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/job"
)

// Jobs drives the queue. The port is declared here because this package is
// what calls it; internal/service/job.Service satisfies it.
type Jobs interface {
	Enqueue(ctx context.Context, target, title string) ([]job.Queued, error)
	List(ctx context.Context, includeCompleted bool, limit int) ([]job.Job, error)
	Cancel(ctx context.Context, id int64) (string, error)
}

// JobServer implements the generated job handler.
type JobServer struct {
	jobs Jobs
	// notFound recognises a job the queue does not hold.
	notFound error
}

// NewJobServer returns the path to mount the handler on, and the handler.
func NewJobServer(
	jobs Jobs,
	notFound error,
	opts ...connect.HandlerOption,
) (string, http.Handler) {
	return ingestv1connect.NewJobServiceHandler(
		&JobServer{jobs: jobs, notFound: notFound},
		opts...,
	)
}

// Enqueue puts everything a target names on the queue.
//
// A directory becomes one job per supported file beneath it, and the
// response names the first, because a caller that queued a library wants an
// id it can follow rather than a list it did not ask for. ListJobs shows the
// rest.
func (s *JobServer) Enqueue(
	ctx context.Context,
	req *connect.Request[ingestv1.EnqueueRequest],
) (*connect.Response[ingestv1.EnqueueResponse], error) {
	if req.Msg.GetSource() == "" {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("source names the file, directory or site to queue"))
	}
	queued, err := s.jobs.Enqueue(ctx, req.Msg.GetSource(), req.Msg.GetTitle())
	if err != nil {
		return nil, s.asConnectError(err)
	}
	if len(queued) == 0 {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("nothing under that source can be read by any adapter"))
	}
	return connect.NewResponse(&ingestv1.EnqueueResponse{
		JobId:         queued[0].JobID,
		QueuePosition: int64(queued[0].Position),
	}), nil
}

func (s *JobServer) ListJobs(
	ctx context.Context,
	req *connect.Request[ingestv1.ListJobsRequest],
) (*connect.Response[ingestv1.ListJobsResponse], error) {
	jobs, err := s.jobs.List(
		ctx, req.Msg.GetIncludeCompleted(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, s.asConnectError(err)
	}
	out := make([]*ingestv1.Job, len(jobs))
	for i, j := range jobs {
		out[i] = jobMessage(j)
	}
	return connect.NewResponse(&ingestv1.ListJobsResponse{Jobs: out}), nil
}

func (s *JobServer) CancelJob(
	ctx context.Context,
	req *connect.Request[ingestv1.CancelJobRequest],
) (*connect.Response[ingestv1.CancelJobResponse], error) {
	if req.Msg.GetJobId() == 0 {
		return nil, connect.NewError(
			connect.CodeInvalidArgument, errors.New("job_id names the job to cancel"))
	}
	status, err := s.jobs.Cancel(ctx, req.Msg.GetJobId())
	if err != nil {
		return nil, s.asConnectError(err)
	}
	return connect.NewResponse(&ingestv1.CancelJobResponse{Status: status}), nil
}

// asConnectError says what the caller should do about a failure.
//
// A target outside the library root, or one no adapter reads, is the
// caller's mistake rather than the server's moment.
func (s *JobServer) asConnectError(err error) error {
	switch {
	case s.notFound != nil && errors.Is(err, s.notFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ingest.ErrUnsupportedFormat):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// jobMessage is one queue row as a caller sees it.
func jobMessage(j job.Job) *ingestv1.Job {
	return &ingestv1.Job{
		JobId:           j.JobID,
		Source:          j.Source,
		Title:           j.Title,
		DocId:           j.DocID,
		Status:          j.Status,
		Phase:           j.Phase,
		ProgressCurrent: optionalInt(j.ProgressCurrent),
		ProgressTotal:   optionalInt(j.ProgressTotal),
		Attempts:        int64(j.Attempts),
		Permanent:       j.Permanent,
		Error:           j.Error,
		Quality:         typev1.Quality(j.Quality),
		Warnings:        j.Warnings,
		CreatedAt:       j.CreatedAt,
		UpdatedAt:       j.UpdatedAt,
	}
}
