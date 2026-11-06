package connectapi

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	ingestv1 "github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1/ingestv1connect"
	typev1 "github.com/bamsammich/docsearch/internal/api/docsearch/type/v1"
	"github.com/bamsammich/docsearch/internal/libroot"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/job"
)

// Jobs drives the queue. The port is declared here because this package is
// what calls it; internal/service/job.Service satisfies it.
type Jobs interface {
	Enqueue(ctx context.Context, target, title string, force bool) ([]job.Queued, error)
	List(ctx context.Context, includeCompleted bool, limit int) ([]job.Job, error)
	Cancel(ctx context.Context, id int64) (string, error)
}

// JobsFor builds one library's ingest queue, as one user.
type JobsFor func(scope pgsession.Scope) (Jobs, error)

// JobServer implements the generated job handler.
type JobServer struct {
	jobs JobsFor
	// readable is every library the asking user reads, which is how a
	// library label becomes a scope.
	readable ReadableFor
	// notFound recognises a job the queue does not hold.
	notFound error
}

// NewJobServer returns the path to mount the handler on, and the handler.
func NewJobServer(
	jobs JobsFor,
	readable ReadableFor,
	notFound error,
	opts ...connect.HandlerOption,
) (string, http.Handler) {
	return ingestv1connect.NewJobServiceHandler(
		&JobServer{jobs: jobs, readable: readable, notFound: notFound},
		opts...,
	)
}

// Enqueue puts everything a target names on the queue.
//
// A directory becomes one job per supported file beneath it, and the
// response names every one, in the order they were queued, because a caller
// that queued a library has to be able to say what it got.
func (s *JobServer) Enqueue(
	ctx context.Context,
	req *connect.Request[ingestv1.EnqueueRequest],
) (*connect.Response[ingestv1.EnqueueResponse], error) {
	if req.Msg.GetSource() == "" {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("source names the file, directory or site to queue"))
	}
	queue, err := s.editing(ctx, req.Msg.GetLibrary())
	if err != nil {
		return nil, err
	}
	queued, err := queue.Enqueue(
		ctx, req.Msg.GetSource(), req.Msg.GetTitle(), req.Msg.GetForce())
	if err != nil {
		return nil, s.asConnectError(err)
	}
	if len(queued) == 0 {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("nothing under that source can be read by any adapter"))
	}
	jobs := make([]*ingestv1.QueuedJob, len(queued))
	for i, q := range queued {
		jobs[i] = &ingestv1.QueuedJob{
			Source:        q.Source,
			JobId:         q.JobID,
			QueuePosition: int64(q.Position),
		}
	}
	return connect.NewResponse(&ingestv1.EnqueueResponse{Jobs: jobs}), nil
}

func (s *JobServer) ListJobs(
	ctx context.Context,
	req *connect.Request[ingestv1.ListJobsRequest],
) (*connect.Response[ingestv1.ListJobsResponse], error) {
	sh, err := labelled(ctx, s.readable, req.Msg.GetLibrary())
	if err != nil {
		return nil, err
	}
	queue, err := s.in(sh)
	if err != nil {
		return nil, err
	}
	jobs, err := queue.List(
		ctx, req.Msg.GetIncludeCompleted(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, s.asConnectError(err)
	}
	out := make([]*ingestv1.Job, len(jobs))
	for i, j := range jobs {
		out[i] = jobMessage(j, sh.library)
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
	queue, err := s.editing(ctx, req.Msg.GetLibrary())
	if err != nil {
		return nil, err
	}
	status, err := queue.Cancel(ctx, req.Msg.GetJobId())
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
	case errors.Is(err, ingest.ErrUnsupportedFormat), errors.Is(err, libroot.ErrOutsideRoot):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// jobMessage is one queue row as a caller sees it.
func jobMessage(j job.Job, lib pgstore.Library) *ingestv1.Job {
	return &ingestv1.Job{
		JobId:           j.JobID,
		Source:          j.Source,
		Title:           j.Title,
		DocId:           qualified(lib, j.DocID),
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

// in is one library's queue.
//
//nolint:ireturn // a factory for a port returns that port; a concrete type here would defeat it.
func (s *JobServer) in(sh shelf) (Jobs, error) {
	jobs, err := s.jobs(sh.scope)
	if err != nil {
		return nil, asConnectError(err)
	}
	return jobs, nil
}

// editing is the queue of a library the user edits, named by its label.
//
//nolint:ireturn // a factory for a port returns that port; a concrete type here would defeat it.
func (s *JobServer) editing(ctx context.Context, label string) (Jobs, error) {
	sh, err := labelled(ctx, s.readable, label)
	if err != nil {
		return nil, err
	}
	if err := sh.editable(); err != nil {
		return nil, err
	}
	return s.in(sh)
}

// qualified is a job's document in the form a caller names it by, and
// nothing for a job that has not named its document yet.
func qualified(lib pgstore.Library, docID string) string {
	if docID == "" {
		return ""
	}
	return lib.Qualify(docID)
}
