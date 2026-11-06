package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Queue is the jobs one person queued in one library. A worker writes as
// User in Library, so the database checks each write against that person's
// grant there.
type Queue struct {
	User    string
	Library string
}

// Queues lists the queues holding work.
type Queues interface {
	Waiting(ctx context.Context) ([]Queue, error)
}

// Rotation is one worker serving every library, a job from each queue in
// turn.
//
// A job writes into one library as the person who queued it, and row-level
// security binds each statement to that pair, so a worker for one queue is
// the natural unit. A deployment gains queues as people sign up and share
// libraries, though, and a worker for each would have to be started by
// hand. A rotation lists the waiting queues each round, so a new one is
// served from its first job, and takes at most one job from each before
// going round again, so one queue's long backlog cannot starve the rest.
type Rotation struct {
	queues Queues
	worker func(Queue) *Service
	log    *slog.Logger
	poll   time.Duration
	once   bool
}

// NewRotation serves every waiting queue, building each one's worker with
// worker. A round that finds no work waits poll; once stops after it.
func NewRotation(
	queues Queues,
	worker func(Queue) *Service,
	poll time.Duration,
	once bool,
	log *slog.Logger,
) *Rotation {
	if poll == 0 {
		poll = DefaultPoll
	}
	return &Rotation{queues: queues, worker: worker, poll: poll, once: once, log: log}
}

// Run takes turns until the context is cancelled.
func (r *Rotation) Run(ctx context.Context) error {
	built := map[Queue]*Service{}
	for ctx.Err() == nil {
		done, err := r.turn(ctx, built)
		if err != nil || done {
			return err
		}
	}
	return nil
}

// turn is one round and the wait after it, and reports whether to stop.
func (r *Rotation) turn(ctx context.Context, built map[Queue]*Service) (bool, error) {
	worked, err := r.round(ctx, built)
	switch {
	case err != nil:
		return true, err
	case worked:
		return false, nil
	case r.once:
		return true, nil
	default:
		return sleep(ctx, r.poll) != nil, nil
	}
}

// round offers each waiting queue one job and reports whether any had one.
func (r *Rotation) round(ctx context.Context, built map[Queue]*Service) (bool, error) {
	queues, err := r.queues.Waiting(ctx)
	if err != nil {
		return false, fmt.Errorf("list waiting queues: %w", err)
	}
	worked := false
	for _, queue := range queues {
		service, ok := built[queue]
		if !ok {
			service = r.worker(queue)
			built[queue] = service
		}
		did, err := service.Next(ctx)
		if err != nil {
			return false, fmt.Errorf("library %s, queued by %s: %w", queue.Library, queue.User, err)
		}
		worked = worked || did
	}
	return worked, nil
}
