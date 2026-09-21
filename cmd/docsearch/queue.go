package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/service/job"
)

// Ported from python/docsearch/cli.py.

func enqueueCommand() *cli.Command {
	return &cli.Command{
		Name:      "enqueue",
		Usage:     "queue a file, directory or documentation site for the worker",
		ArgsUsage: argTarget,
		Description: "Returns as soon as the work is queued. `docsearch add` is the " +
			"other path: it waits, and prints what the ingest is doing.",
		Flags:  append(serverFlags(), titleFlag()),
		Action: enqueueTarget,
	}
}

func enqueueTarget(ctx context.Context, cmd *cli.Command) error {
	target := cmd.Args().First()
	if target == "" {
		return errNoTarget
	}
	queued, err := clientFor(cmd).Enqueue(ctx, target, cmd.String(flagTitle))
	if err != nil {
		return err
	}
	for _, q := range queued {
		fmt.Printf("queued job %d: %s\n", q.JobID, q.Source)
	}
	return nil
}

func jobsCommand() *cli.Command {
	return &cli.Command{
		Name:  "jobs",
		Usage: "show the ingest queue",
		Flags: append(serverFlags(),
			&cli.BoolFlag{
				Name:  "all",
				Usage: "include jobs that have finished",
			},
			&cli.IntFlag{
				Name:  "limit",
				Usage: "how many jobs to show",
			},
		),
		Action: showJobs,
	}
}

func showJobs(ctx context.Context, cmd *cli.Command) error {
	jobs, err := clientFor(cmd).Jobs(ctx, cmd.Bool("all"), cmd.Int("limit"))
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Println("no jobs")
		return nil
	}
	fmt.Printf("%5s %-10s %-9s %12s %3s %-9s SOURCE\n",
		"ID", "STATUS", "PHASE", "PROGRESS", "TRY", "QUALITY")
	for _, j := range jobs {
		fmt.Printf("%5d %-10s %-9s %12s %3d %-9s %s\n",
			j.JobID, j.Status, dash(j.Phase), progress(j), j.Attempts,
			jobQuality(j), filepath.Base(j.Source))
		if j.Error != "" {
			fmt.Printf("        error: %s\n", j.Error)
		}
		for _, warning := range j.Warnings {
			fmt.Printf("        warning: %s\n", warning)
		}
	}
	return nil
}

func cancelCommand() *cli.Command {
	return &cli.Command{
		Name:      "cancel",
		Usage:     "ask a queued or running job to stop",
		ArgsUsage: "<job_id>",
		Description: "Reports what the job reads as now. A job that already finished " +
			"is not cancelled, and saying so beats claiming a cancellation that " +
			"did nothing.",
		Flags:  serverFlags(),
		Action: cancelJob,
	}
}

func cancelJob(ctx context.Context, cmd *cli.Command) error {
	id := cmd.Args().First()
	if id == "" {
		return errNoJobID
	}
	var jobID int64
	if _, err := fmt.Sscanf(id, "%d", &jobID); err != nil || jobID <= 0 {
		return fmt.Errorf("not a job id: %s", id)
	}
	status, err := clientFor(cmd).Cancel(ctx, jobID)
	if err != nil {
		return err
	}
	fmt.Printf("job %d: %s\n", jobID, status)
	return nil
}

// progress is how far a job has got, and a dash for one that has not
// started, which is not progress of zero.
func progress(j job.Job) string {
	if j.ProgressCurrent == nil {
		return "-"
	}
	if j.ProgressTotal == nil || *j.ProgressTotal == 0 {
		return fmt.Sprint(*j.ProgressCurrent)
	}
	return fmt.Sprintf("%d/%d", *j.ProgressCurrent, *j.ProgressTotal)
}

// jobQuality is the grade the finished job recorded, or a dash.
func jobQuality(j job.Job) string {
	if j.Quality == 0 {
		return "-"
	}
	return j.Quality.String()
}

// dash stands in for a field a job has not filled.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
