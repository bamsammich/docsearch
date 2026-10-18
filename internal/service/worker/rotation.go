package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Accounts lists every library there is.
type Accounts interface {
	Users(ctx context.Context) ([]string, error)
}

// Rotation is one worker serving every library, a job from each in turn.
//
// A job writes into one library, and row-level security binds each
// statement to one user, so a worker for a single library is the natural
// unit. A deployment with accounts gains one with every sign-up, though, and
// a worker per account would have to be started for each. A rotation lists
// the accounts each round, so a new one is served from its first upload,
// and takes at most one job from each before going round again, so one
// library's long queue cannot starve the rest.
type Rotation struct {
	accounts Accounts
	worker   func(user string) *Service
	log      *slog.Logger
	poll     time.Duration
	once     bool
}

// NewRotation serves every account, building each library's worker with
// worker. A round that finds no work waits poll; once stops after it.
func NewRotation(
	accounts Accounts,
	worker func(user string) *Service,
	poll time.Duration,
	once bool,
	log *slog.Logger,
) *Rotation {
	if poll == 0 {
		poll = DefaultPoll
	}
	return &Rotation{accounts: accounts, worker: worker, poll: poll, once: once, log: log}
}

// Run takes turns until the context is cancelled.
func (r *Rotation) Run(ctx context.Context) error {
	built := map[string]*Service{}
	for ctx.Err() == nil {
		done, err := r.turn(ctx, built)
		if err != nil || done {
			return err
		}
	}
	return nil
}

// turn is one round and the wait after it, and reports whether to stop.
func (r *Rotation) turn(ctx context.Context, built map[string]*Service) (bool, error) {
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

// round offers each library one job and reports whether any had one.
func (r *Rotation) round(ctx context.Context, built map[string]*Service) (bool, error) {
	users, err := r.accounts.Users(ctx)
	if err != nil {
		return false, fmt.Errorf("list accounts: %w", err)
	}
	worked := false
	for _, user := range users {
		service, ok := built[user]
		if !ok {
			service = r.worker(user)
			built[user] = service
		}
		did, err := service.Next(ctx)
		if err != nil {
			return false, fmt.Errorf("library %s: %w", user, err)
		}
		worked = worked || did
	}
	return worked, nil
}
