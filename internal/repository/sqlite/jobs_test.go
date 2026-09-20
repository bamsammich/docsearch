package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/repository/sqlite"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/worker"
)

const lease = 5 * time.Minute

// JobsSuite runs the queue against a real database. The claim is one
// statement whose whole purpose is what happens when two workers race, and
// only a database shows that.
type JobsSuite struct {
	suite.Suite
	jobs *sqlite.Jobs
	repo *sqlite.Repository
	db   *sql.DB
}

func TestJobs(t *testing.T) { suite.Run(t, new(JobsSuite)) }

func (s *JobsSuite) SetupTest() {
	db, err := sqlite.Open(filepath.Join(s.T().TempDir(), "index.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })

	s.Require().NoError(schema.Create(s.T().Context(), db))

	s.db = db
	s.jobs = sqlite.NewJobs(db)
	s.repo = sqlite.New(db)
}

// enqueue puts one job on the queue and returns its id.
func (s *JobsSuite) enqueue(sourcePath string) int64 {
	res, err := s.db.Exec(
		`INSERT INTO ingest_jobs (source_path, status, created_at, updated_at)
		 VALUES (?, 'queued', datetime('now'), datetime('now'))`, sourcePath)
	s.Require().NoError(err)
	id, err := res.LastInsertId()
	s.Require().NoError(err)
	return id
}

func (s *JobsSuite) claim() *worker.Job {
	job, err := s.jobs.Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	return job
}

// queryOne reads one scalar from the queue.
func (s *JobsSuite) queryOne(query string, args ...any) string {
	var value sql.NullString
	s.Require().NoError(s.db.QueryRow(query, args...).Scan(&value))
	return value.String
}

func (s *JobsSuite) TestAnEmptyQueueYieldsNothing() {
	s.Nil(s.claim())
}

func (s *JobsSuite) TestClaimingTakesTheOldestAndCountsTheAttempt() {
	first := s.enqueue("/library/a.md")
	s.enqueue("/library/b.md")

	job := s.claim()
	s.Require().NotNil(job)
	s.Equal(first, job.ID)
	s.Equal("/library/a.md", job.Source)
	s.Equal(1, job.Attempts)
	s.Equal("running", s.queryOne(`SELECT status FROM ingest_jobs WHERE id=?`, first))
	s.NotEmpty(s.queryOne(`SELECT lease_until FROM ingest_jobs WHERE id=?`, first))
}

func (s *JobsSuite) TestTwoClaimsNeverTakeTheSameJob() {
	first := s.enqueue("/library/a.md")
	second := s.enqueue("/library/b.md")

	one, two := s.claim(), s.claim()
	s.Require().NotNil(one)
	s.Require().NotNil(two)
	s.Equal(first, one.ID)
	s.Equal(second, two.ID)
	s.Nil(s.claim(), "both are running and neither lease has expired")
}

func (s *JobsSuite) TestAJobWhoseLeaseExpiredIsTakenBack() {
	// A worker killed mid-job is recovered by the next one without anyone
	// intervening, which is what the lease is for.
	id := s.enqueue("/library/a.md")
	s.Require().NotNil(s.claim())
	_, err := s.db.Exec(
		`UPDATE ingest_jobs SET lease_until = datetime('now', '-1 minute') WHERE id=?`, id)
	s.Require().NoError(err)

	job := s.claim()
	s.Require().NotNil(job)
	s.Equal(id, job.ID)
	s.Equal(2, job.Attempts, "the reclaim counts as an attempt")
}

func (s *JobsSuite) TestAJobAtTheAttemptCeilingIsNotClaimed() {
	// Excluded by the claim rather than after it: a permanently failing job
	// that is claimed and put back would be reclaimed forever and starve
	// everything behind it.
	id := s.enqueue("/library/a.md")
	_, err := s.db.Exec(`UPDATE ingest_jobs SET attempts = 3 WHERE id=?`, id)
	s.Require().NoError(err)

	s.Nil(s.claim())
}

func (s *JobsSuite) TestAJobTheOperatorCancelledIsNotClaimed() {
	id := s.enqueue("/library/a.md")
	_, err := s.db.Exec(`UPDATE ingest_jobs SET cancel_req = 1 WHERE id=?`, id)
	s.Require().NoError(err)

	s.Nil(s.claim())
}

func (s *JobsSuite) TestAClaimClearsWhatTheLastAttemptLeft() {
	id := s.enqueue("/library/a.md")
	_, err := s.db.Exec(
		`UPDATE ingest_jobs SET phase='fetch', progress_cur=3, progress_tot=9,
		        error='database is locked' WHERE id=?`, id)
	s.Require().NoError(err)

	s.Require().NotNil(s.claim())
	s.Empty(s.queryOne(`SELECT phase FROM ingest_jobs WHERE id=?`, id))
	s.Empty(s.queryOne(`SELECT progress_cur FROM ingest_jobs WHERE id=?`, id))
	s.Empty(s.queryOne(`SELECT error FROM ingest_jobs WHERE id=?`, id))
}

func (s *JobsSuite) TestProgressRenewsTheLease() {
	// A job that is visibly advancing must never be reclaimed out from under
	// the worker running it.
	id := s.enqueue("/library/a.md")
	s.Require().NotNil(s.claim())
	_, err := s.db.Exec(
		`UPDATE ingest_jobs SET lease_until = datetime('now', '-1 minute') WHERE id=?`, id)
	s.Require().NoError(err)

	s.Require().NoError(
		s.jobs.RecordProgress(s.T().Context(), id, "fetch", 3, 9, lease))

	s.Equal("fetch", s.queryOne(`SELECT phase FROM ingest_jobs WHERE id=?`, id))
	s.Equal("3", s.queryOne(`SELECT progress_cur FROM ingest_jobs WHERE id=?`, id))
	s.Nil(s.claim(), "the renewed lease keeps the job out of reach")
}

func (s *JobsSuite) TestACancelRequestIsSeenByTheWorkerRunningTheJob() {
	id := s.enqueue("/library/a.md")
	requested, err := s.jobs.CancelRequested(s.T().Context(), id)
	s.Require().NoError(err)
	s.False(requested)

	_, err = s.db.Exec(`UPDATE ingest_jobs SET cancel_req = 1 WHERE id=?`, id)
	s.Require().NoError(err)

	requested, err = s.jobs.CancelRequested(s.T().Context(), id)
	s.Require().NoError(err)
	s.True(requested)
}

func (s *JobsSuite) TestAVanishedJobReadsAsCancelled() {
	// Nothing is waiting for a job whose row is gone, so the ingest should
	// stop rather than finish writing a document nobody asked for.
	requested, err := s.jobs.CancelRequested(s.T().Context(), 404)
	s.Require().NoError(err)
	s.True(requested)
}

// writing is a document a job left half written.
func (s *JobsSuite) writing(id int64, docID string) {
	s.Require().NoError(s.jobs.RecordDocID(s.T().Context(), id, docID))
	s.Require().NoError(s.repo.Create(s.T().Context(), ingest.Document{
		DocID: docID, Title: "Guide", Format: "markdown",
		Identity: "/library/a.md", Digest: "abc", Kind: domain.SourceKindFile,
	}, ""))
}

func (s *JobsSuite) TestCancellingRemovesWhatTheJobHalfWrote() {
	id := s.enqueue("/library/a.md")
	s.Require().NotNil(s.claim())
	s.writing(id, "guide")

	s.Require().NoError(s.jobs.Cancel(s.T().Context(), id))

	s.Equal("cancelled", s.queryOne(`SELECT status FROM ingest_jobs WHERE id=?`, id))
	s.Equal("0", s.queryOne(`SELECT COUNT(*) FROM documents WHERE doc_id='guide'`))
}

func (s *JobsSuite) TestAPreviouslyGoodDocumentSurvivesAFailedReingest() {
	// A ready row under the identifier the job was writing is a previous
	// good ingest, and a failed attempt must not take it with it.
	id := s.enqueue("/library/a.md")
	s.Require().NotNil(s.claim())
	s.writing(id, "guide")
	s.Require().NoError(s.repo.MarkReady(s.T().Context(), ingest.Ready{
		IngestedAt: stamped, DocID: "guide", ChunkCount: 1,
	}))

	s.Require().NoError(
		s.jobs.Finish(s.T().Context(), id, worker.Failed, "disk full", false))

	s.Equal("ready", s.queryOne(`SELECT status FROM documents WHERE doc_id='guide'`))
	s.Equal("failed", s.queryOne(`SELECT status FROM ingest_jobs WHERE id=?`, id))
}

func (s *JobsSuite) TestARequeuedJobIsClaimableAgain() {
	id := s.enqueue("/library/a.md")
	s.Require().NotNil(s.claim())

	s.Require().NoError(
		s.jobs.Finish(s.T().Context(), id, worker.Requeued, "database is locked", false))

	s.Equal("queued", s.queryOne(`SELECT status FROM ingest_jobs WHERE id=?`, id))
	s.Equal("0", s.queryOne(`SELECT permanent FROM ingest_jobs WHERE id=?`, id))

	again := s.claim()
	s.Require().NotNil(again)
	s.Equal(2, again.Attempts)
}

func (s *JobsSuite) TestAPermanentFailureIsRecordedAsOne() {
	// attempts keeps its true value: a job that failed deterministically on
	// its first attempt must stay distinguishable from one that exhausted
	// three, and the permanent flag is what carries the difference.
	id := s.enqueue("/library/a.md")
	s.Require().NotNil(s.claim())

	s.Require().NoError(
		s.jobs.Finish(s.T().Context(), id, worker.Failed, "produced no chunks", true))

	s.Equal("failed", s.queryOne(`SELECT status FROM ingest_jobs WHERE id=?`, id))
	s.Equal("1", s.queryOne(`SELECT permanent FROM ingest_jobs WHERE id=?`, id))
	s.Equal("1", s.queryOne(`SELECT attempts FROM ingest_jobs WHERE id=?`, id))
	s.Contains(s.queryOne(`SELECT error FROM ingest_jobs WHERE id=?`, id), "no chunks")
}

func (s *JobsSuite) TestAnUnchangedSourceCompletesItsJob() {
	id := s.enqueue("/library/copy.md")
	s.Require().NotNil(s.claim())

	s.Require().NoError(
		s.jobs.CompleteWithoutDocument(s.T().Context(), id, "guide"))

	s.Equal("done", s.queryOne(`SELECT status FROM ingest_jobs WHERE id=?`, id))
	s.Equal("guide", s.queryOne(`SELECT doc_id FROM ingest_jobs WHERE id=?`, id))
	s.Empty(s.queryOne(`SELECT lease_until FROM ingest_jobs WHERE id=?`, id))
}
