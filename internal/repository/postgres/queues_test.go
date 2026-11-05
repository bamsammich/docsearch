package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/service/worker"
)

// QueuesSuite runs a shared library's queue against a real database: who
// queued a job decides who the worker writes as, and a job whose requester
// can no longer add to the library is answered rather than left to wait.
type QueuesSuite struct {
	suite.Suite
	db *pgtest.DB
}

func TestQueues(t *testing.T) { suite.Run(t, new(QueuesSuite)) }

// The shared library's admin is the built-in account, and second edits it.
var (
	admin  = pgsession.Scope{User: builtinID, Library: "lib_team"}
	editor = pgsession.Scope{User: "second", Library: "lib_team"}
)

func (s *QueuesSuite) SetupTest() {
	s.db = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.db.Owner))
	for _, statement := range []string{
		`SELECT create_account('second', 'https://issuer.example', 'subject-2')`,
		`INSERT INTO libraries (library_id, kind, slug, name)
		   VALUES ('lib_team', 'shared', 'team', 'Team docs')`,
		`CREATE TABLE chunks_lib_team PARTITION OF chunks FOR VALUES IN ('lib_team')`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON chunks_lib_team TO docsearch_app`,
		`INSERT INTO library_grants (library_id, user_id, role)
		   VALUES ('lib_team', 'default', 'admin'), ('lib_team', 'second', 'editor')`,
	} {
		_, err := s.db.Owner.ExecContext(s.T().Context(), statement)
		s.Require().NoError(err)
	}
}

// queued puts one source on the queue as scope and returns its id.
func (s *QueuesSuite) queued(scope pgsession.Scope, source string) int64 {
	id, _, err := postgres.NewJobs(s.db.App, scope).Add(s.T().Context(), source, "", false)
	s.Require().NoError(err)
	return id
}

func (s *QueuesSuite) waiting() []worker.Queue {
	queues, err := postgres.NewQueues(s.db.App).Waiting(s.T().Context())
	s.Require().NoError(err)
	return queues
}

func (s *QueuesSuite) TestAQueueIsTheLibraryAndWhoeverQueuedIt() {
	s.queued(editor, "https://example.com/docs")
	s.queued(pgsession.Personal(builtinID), "/library/guide.md")

	s.Equal([]worker.Queue{
		{User: builtinID, Library: builtinID},
		{User: "second", Library: "lib_team"},
	}, s.waiting())
}

func (s *QueuesSuite) TestAWorkerClaimsOnlyTheJobsOfWhomItWritesAs() {
	// The admin can read the queue, but the job is second's to write.
	s.queued(editor, "https://example.com/docs")

	theirs, err := postgres.NewJobs(s.db.App, admin).Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Nil(theirs)

	mine, err := postgres.NewJobs(s.db.App, editor).Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Require().NotNil(mine)
	s.Equal("https://example.com/docs", mine.Source)
}

func (s *QueuesSuite) TestAJobWhoseRequesterCanNoLongerAddFails() {
	id := s.queued(editor, "https://example.com/docs")
	_, err := s.db.Owner.ExecContext(
		s.T().Context(),
		`UPDATE library_grants SET role = 'reader' WHERE library_id = 'lib_team' AND user_id = 'second'`,
	)
	s.Require().NoError(err)

	s.Empty(s.waiting(), "no one can be written as for the job")

	got, err := postgres.NewJobs(s.db.App, admin).Job(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal("failed", got.Status)
	s.Contains(got.Error, "can no longer add to this library")
}

func (s *QueuesSuite) TestAJobLeftRunningFailsOnceItsLeaseRunsOut() {
	id := s.queued(editor, "https://example.com/docs")
	claimed, err := postgres.NewJobs(s.db.App, editor).Claim(s.T().Context(), lease, 3)
	s.Require().NoError(err)
	s.Require().NotNil(claimed)
	_, err = s.db.Owner.ExecContext(
		s.T().Context(),
		`DELETE FROM library_grants WHERE library_id = 'lib_team' AND user_id = 'second'`,
	)
	s.Require().NoError(err)

	s.Empty(s.waiting())
	running, err := postgres.NewJobs(s.db.App, admin).Job(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal("running", running.Status, "a worker may still be finishing it")

	_, err = s.db.Owner.ExecContext(
		s.T().Context(),
		`UPDATE ingest_jobs SET lease_until = now() - interval '1 minute' WHERE id = $1`,
		id,
	)
	s.Require().NoError(err)
	s.Empty(s.waiting())

	got, err := postgres.NewJobs(s.db.App, admin).Job(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal("failed", got.Status)
	s.Contains(got.Error, "can no longer add to this library")
}

func (s *QueuesSuite) TestAReaderCannotQueue() {
	_, err := s.db.Owner.ExecContext(
		s.T().Context(),
		`UPDATE library_grants SET role = 'reader' WHERE library_id = 'lib_team' AND user_id = 'second'`,
	)
	s.Require().NoError(err)

	_, _, err = postgres.NewJobs(s.db.App, editor).
		Add(s.T().Context(), "https://example.com/docs", "", false)
	s.Require().Error(err)
}
