package mcpserver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/job"
)

// StatusSuite covers what ingest_status derives from a queue row.
type StatusSuite struct{ suite.Suite }

func TestStatus(t *testing.T) { suite.Run(t, new(StatusSuite)) }

var at = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func ints(cur, tot int) (*int, *int) { return &cur, &tot }

func (s *StatusSuite) TestARunningJobReportsItsProgressAndHowLongItHasRun() {
	cur, tot := ints(3, 12)
	got := statusOf(job.Job{
		Status: statusRunning, ProgressCurrent: cur, ProgressTotal: tot,
		CreatedAt: "2026-10-17T11:58:30Z", UpdatedAt: "2026-10-17T11:59:00Z",
		LeaseUntil: at.Add(time.Minute), Quality: domain.QualityOK,
	}, at)
	s.Equal("3/12 (25%)", got.Progress)
	s.Equal("1m30s", got.Elapsed, "a running job is timed to now")
	s.Equal("ok", got.Quality)
	s.False(got.Stalled)
}

// A running job whose lease lapsed has most likely lost its worker, and its
// last progress should not read as live.
func (s *StatusSuite) TestARunningJobPastItsLeaseIsStalled() {
	got := statusOf(job.Job{
		Status: statusRunning, CreatedAt: "2026-10-17T11:00:00Z",
		LeaseUntil: at.Add(-time.Minute),
	}, at)
	s.True(got.Stalled)
	s.Contains(got.StalledNote, "2026-10-17T11:59:00Z")
}

func (s *StatusSuite) TestAFailureSaysWhetherRetryingCouldHaveHelped() {
	permanent := statusOf(job.Job{Status: statusFailed, Attempts: 1, Permanent: true}, at)
	s.Contains(permanent.Disposition, "failed permanently after 1 attempt")
	exhausted := statusOf(job.Job{Status: statusFailed, Attempts: 3}, at)
	s.Contains(exhausted.Disposition, "exhausted after 3 attempt")
	done := statusOf(job.Job{Status: "done"}, at)
	s.Empty(done.Disposition)
}

// A finished job is timed to its last update, not to now.
func (s *StatusSuite) TestAFinishedJobIsTimedToItsEnd() {
	got := statusOf(job.Job{
		Status: "done", CreatedAt: "2026-10-17T10:00:00Z", UpdatedAt: "2026-10-17T10:05:00Z",
	}, at)
	s.Equal("5m0s", got.Elapsed)
	s.Empty(got.Quality, "an ungraded job shows no grade")
}
