// Package job drives the ingest queue the worker reads.
//
// The queue is the path a caller takes when it does not want to wait.
// internal/service/ingest is the other one: it runs the ingest there and
// then, and the caller watches it.
//
// Ported from python/docsearch/cli.py.
package job

import (
	"context"
	"fmt"

	"github.com/bamsammich/docsearch/internal/domain"
)

// defaultLimit is how many jobs a listing returns when the caller names no
// bound. Enough to see a morning's work without paging.
const defaultLimit = 50

// Job is one row of the queue.
type Job struct {
	ProgressCurrent *int
	ProgressTotal   *int
	Source          string
	Title           string
	DocID           string
	// Status is 'queued', 'running', 'done', 'failed' or 'cancelled'.
	Status    string
	Phase     string
	Error     string
	CreatedAt string
	UpdatedAt string
	Warnings  []string
	JobID     int64
	Attempts  int
	// Permanent marks a failure another attempt would not survive.
	Permanent bool
	Quality   domain.Quality
}

// Queue is the stored queue. The port is declared here because this package
// is what drives it.
type Queue interface {
	// Add puts one source on the queue and returns its id and its position,
	// counting itself.
	Add(ctx context.Context, source, title string) (id int64, position int, err error)
	List(ctx context.Context, includeCompleted bool, limit int) ([]Job, error)
	// RequestCancel asks a job to stop and reports what it reads as now.
	RequestCancel(ctx context.Context, id int64) (status string, err error)
}

// Targets turns one command-line target into the sources it names.
type Targets interface {
	Targets(target string) ([]string, error)
}

// Queued is one job the caller just created.
type Queued struct {
	Source   string
	JobID    int64
	Position int
}

// Service drives the queue.
type Service struct {
	queue   Queue
	targets Targets
}

// New builds the service over the queue and the source registry.
func New(queue Queue, targets Targets) *Service {
	return &Service{queue: queue, targets: targets}
}

// Enqueue puts everything a target names on the queue.
//
// A directory becomes one job per supported file beneath it, because the
// site model applies to a crawled site rather than to any directory that
// happens to hold Markdown.
func (s *Service) Enqueue(ctx context.Context, target, title string) ([]Queued, error) {
	sources, err := s.targets.Targets(target)
	if err != nil {
		return nil, err
	}
	queued := make([]Queued, 0, len(sources))
	for _, source := range sources {
		id, position, err := s.queue.Add(ctx, source, title)
		if err != nil {
			return nil, fmt.Errorf("queue %s: %w", source, err)
		}
		queued = append(queued, Queued{Source: source, JobID: id, Position: position})
	}
	return queued, nil
}

// List is the queue, newest first.
func (s *Service) List(ctx context.Context, includeCompleted bool, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	jobs, err := s.queue.List(ctx, includeCompleted, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return jobs, nil
}

// Cancel asks a running job to stop, and reports what it reads as now.
//
// A job that already finished is not cancelled, and saying so beats
// reporting a cancellation that did nothing.
func (s *Service) Cancel(ctx context.Context, id int64) (string, error) {
	status, err := s.queue.RequestCancel(ctx, id)
	if err != nil {
		return "", fmt.Errorf("cancel job %d: %w", id, err)
	}
	return status, nil
}
