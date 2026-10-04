package schema_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

// IsolationSuite runs the four checks docs/research/postgres-spike.md
// settled, against the role a request uses: one that owns nothing and lacks
// BYPASSRLS, which is the only role the policies apply to in a deployment.
//
// Row-level security guards a query that forgot its filter. It does not
// guard a compromised application, which can set any user, so what is
// checked here is narrow on purpose: that a session which says nothing sees
// nothing, that a query without a filter still sees one library, and that
// the restricted role cannot reach past the privileges it was granted.
type IsolationSuite struct {
	suite.Suite
	db *pgtest.DB
}

func TestIsolation(t *testing.T) { suite.Run(t, new(IsolationSuite)) }

func (s *IsolationSuite) SetupTest() {
	s.db = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.db.Owner))

	// Two libraries. The second user needs a partition, which is what
	// creating a user does once there is a service to do it.
	s.exec(`INSERT INTO users (user_id) VALUES ('second')`)
	s.exec(`CREATE TABLE chunks_second PARTITION OF chunks FOR VALUES IN ('second')`)
	s.exec(`GRANT SELECT, INSERT, UPDATE, DELETE ON chunks_second TO ` + pgtest.AppRole)

	s.library("default", "shared-manual", "the dimmer curve is set per channel")
	s.library("second", "shared-manual", "a different document under the same id")
}

func (s *IsolationSuite) exec(statement string, args ...any) {
	_, err := s.db.Owner.ExecContext(s.T().Context(), statement, args...)
	s.Require().NoError(err)
}

// library writes one document, one chunk, and the crawl cache behind them for
// a user. The write names the user because FORCE binds the owner too.
func (s *IsolationSuite) library(userID, docID, text string) {
	pgtest.WriteAs(s.T(), s.db.Owner, userID, func(tx *sql.Tx) {
		s.write(tx,
			`INSERT INTO documents
			   (user_id, doc_id, title, format, source_path, sha256, status)
			 VALUES ($1, $2, 'A Manual', 'pdf', '/library/manual.pdf', 'abc', 'ready')`,
			userID, docID)
		s.write(tx,
			`INSERT INTO chunks (user_id, doc_id, ordinal, heading_path, text)
			 VALUES ($1, $2, 0, '4. Dimmer curves', $3)`, userID, docID, text)
		s.write(tx,
			`INSERT INTO responses
			   (user_id, url, final_url, status, body, sha256, fetched_at)
			 VALUES ($1, $2, $2, 200, $3, 'abc', now())`,
			userID, "https://example.com/"+docID, []byte(text))
		s.write(tx,
			`INSERT INTO robots (user_id, host, body, fetched_at)
			 VALUES ($1, 'example.com', $2, now())`, userID, userID+" asked")
	})
}

// write runs one statement inside a transaction a caller opened.
func (s *IsolationSuite) write(tx *sql.Tx, statement string, args ...any) {
	_, err := tx.ExecContext(s.T().Context(), statement, args...)
	s.Require().NoError(err)
}

func (s *IsolationSuite) TestASessionThatNamesNoUserSeesNothing() {
	for _, table := range []string{
		"documents", "chunks", "pages", "index_terms", "ingest_jobs", "responses", "robots",
	} {
		var n int
		err := s.db.App.QueryRowContext(s.T().Context(),
			`SELECT COUNT(*) FROM `+table).Scan(&n) //nolint:gosec // a constant from this list
		s.Require().NoError(err, table)
		s.Zero(n, table)
	}
}

func (s *IsolationSuite) TestAQueryWithoutAFilterSeesOneLibrary() {
	// The forgotten filter is the case row-level security exists for: the
	// query asks for every document and gets one user's.
	pgtest.ReadAs(s.T(), s.db.App, "default", func(tx *sql.Tx) {
		var text string
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT text FROM chunks`).Scan(&text))
		s.Contains(text, "dimmer curve")
	})
}

func (s *IsolationSuite) TestOneUsersIdentifiersReachNothingOfAnothers() {
	// Both libraries hold a document under the same doc_id, which is what
	// makes a shared identifier space a trap: the second user asking for it
	// must get their own row, never the first user's.
	pgtest.ReadAs(s.T(), s.db.App, "second", func(tx *sql.Tx) {
		var text string
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT text FROM chunks WHERE doc_id = 'shared-manual'`).Scan(&text))
		s.Contains(text, "a different document")
	})
}

func (s *IsolationSuite) TestWritingForAnotherUserIsRefused() {
	// The policy covers writes as well as reads, so a session cannot put a
	// row into a library it cannot see.
	pgtest.ReadAs(s.T(), s.db.App, "default", func(tx *sql.Tx) {
		_, err := tx.ExecContext(s.T().Context(),
			`INSERT INTO documents
			   (user_id, doc_id, title, format, source_path, sha256, status)
			 VALUES ('second', 'smuggled', 'T', 'pdf', '/x', 'abc', 'ready')`)
		s.Require().Error(err)
	})
}

func (s *IsolationSuite) TestUpdatingAnotherUsersRowChangesNothing() {
	// An UPDATE a policy hides is not an error, it is zero rows, which is
	// the answer that leaks nothing about whether the row exists.
	pgtest.ReadAs(s.T(), s.db.App, "default", func(tx *sql.Tx) {
		result, err := tx.ExecContext(s.T().Context(),
			`UPDATE documents SET title = 'renamed' WHERE user_id = 'second'`)
		s.Require().NoError(err)
		affected, err := result.RowsAffected()
		s.Require().NoError(err)
		s.Zero(affected)
	})

	pgtest.ReadAs(s.T(), s.db.Owner, "second", func(tx *sql.Tx) {
		var title string
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT title FROM documents WHERE user_id = 'second'`).Scan(&title))
		s.Equal("A Manual", title)
	})
}

func (s *IsolationSuite) TestTheRestrictedRoleCannotTouchTheSchema() {
	// It owns nothing, so a dropped policy or a disabled table is beyond it
	// however the application is reached.
	_, err := s.db.App.ExecContext(s.T().Context(),
		`ALTER TABLE documents DISABLE ROW LEVEL SECURITY`)
	s.Require().Error(err)

	_, err = s.db.App.ExecContext(s.T().Context(), `DROP POLICY own ON documents`)
	s.Require().Error(err)
}

func (s *IsolationSuite) TestEachUsersChunksLiveInTheirOwnPartition() {
	// One partition per user is what keeps one library's statistics out of
	// another's ranking: BM25 reads its inverse document frequency and
	// average length from the index it searches.
	pgtest.ReadAs(s.T(), s.db.Owner, "second", func(tx *sql.Tx) {
		var partition string
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT tableoid::regclass::text FROM chunks LIMIT 1`).Scan(&partition))
		s.Equal("chunks_second", partition)
	})
}

func (s *IsolationSuite) TestAChunkForAUserWithNoPartitionIsRefused() {
	// A partition is created with the user. Without one the insert fails
	// rather than landing somewhere a search will not look.
	s.exec(`INSERT INTO users (user_id) VALUES ('third')`)

	_, err := s.db.Owner.ExecContext(s.T().Context(),
		`INSERT INTO chunks (user_id, doc_id, ordinal, heading_path, text)
		 VALUES ('third', 'shared-manual', 0, 'H', 'orphan')`)
	s.Require().Error(err)
}
