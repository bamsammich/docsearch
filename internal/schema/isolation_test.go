package schema_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgsession"
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

	// Two libraries. create_account makes the row, the partition and the
	// grant together, which is the only way an account arrives: a chunk for
	// a user with no partition is refused.
	//
	// The second account carries an identity because the built-in one does
	// not, and version 10 allows exactly one account without one.
	s.exec(`SELECT create_account('second', 'https://issuer.example', 'subject-2')`)

	s.library("default", "shared-manual", "the planet orbit is set per sensor")
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
			   (library_id, doc_id, title, format, source_path, sha256, status)
			 VALUES ($1, $2, 'A Manual', 'pdf', '/library/manual.pdf', 'abc', 'ready')`,
			userID, docID)
		s.write(tx,
			`INSERT INTO chunks (library_id, doc_id, ordinal, heading_path, text)
			 VALUES ($1, $2, 0, '4. Planet orbits', $3)`, userID, docID, text)
		s.write(tx,
			`INSERT INTO responses
			   (library_id, url, final_url, status, body, sha256, fetched_at)
			 VALUES ($1, $2, $2, 200, $3, 'abc', now())`,
			userID, "https://example.com/"+docID, []byte(text))
		s.write(tx,
			`INSERT INTO robots (library_id, host, body, fetched_at)
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
		s.Contains(text, "planet orbit")
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
			   (library_id, doc_id, title, format, source_path, sha256, status)
			 VALUES ('second', 'smuggled', 'T', 'pdf', '/x', 'abc', 'ready')`)
		s.Require().Error(err)
	})
}

func (s *IsolationSuite) TestUpdatingAnotherUsersRowChangesNothing() {
	// An UPDATE a policy hides is not an error, it is zero rows, which is
	// the answer that leaks nothing about whether the row exists.
	pgtest.ReadAs(s.T(), s.db.App, "default", func(tx *sql.Tx) {
		result, err := tx.ExecContext(s.T().Context(),
			`UPDATE documents SET title = 'renamed' WHERE library_id = 'second'`)
		s.Require().NoError(err)
		affected, err := result.RowsAffected()
		s.Require().NoError(err)
		s.Zero(affected)
	})

	pgtest.ReadAs(s.T(), s.db.Owner, "second", func(tx *sql.Tx) {
		var title string
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT title FROM documents WHERE library_id = 'second'`).Scan(&title))
		s.Equal("A Manual", title)
	})
}

func (s *IsolationSuite) TestTheRestrictedRoleCannotTouchTheSchema() {
	// It owns nothing, so a dropped policy or a disabled table is beyond it
	// however the application is reached.
	_, err := s.db.App.ExecContext(s.T().Context(),
		`ALTER TABLE documents DISABLE ROW LEVEL SECURITY`)
	s.Require().Error(err)

	_, err = s.db.App.ExecContext(s.T().Context(), `DROP POLICY lib_read ON documents`)
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
	// A partition is created with the user, so this row is made the long way
	// rather than through create_account, which would make one. It carries an
	// identity because only the built-in account may go without.
	s.exec(`INSERT INTO users (user_id, issuer, subject)
		VALUES ('third', 'https://issuer.example', 'subject-3')`)

	_, err := s.db.Owner.ExecContext(s.T().Context(),
		`INSERT INTO chunks (library_id, doc_id, ordinal, heading_path, text)
		 VALUES ('third', 'shared-manual', 0, 'H', 'orphan')`)
	s.Require().Error(err)
}

// shared makes a library owned by no one person, the way a team's is, with
// default as its admin and second as a reader, and one document in it.
func (s *IsolationSuite) shared() {
	s.exec(`INSERT INTO libraries (library_id, kind, slug, name)
		VALUES ('lib_team', 'shared', 'team', 'Team docs')`)
	s.exec(`CREATE TABLE chunks_lib_team PARTITION OF chunks FOR VALUES IN ('lib_team')`)
	s.exec(`GRANT SELECT, INSERT, UPDATE, DELETE ON chunks_lib_team TO docsearch_app`)
	s.exec(`INSERT INTO library_grants (library_id, user_id, role)
		VALUES ('lib_team', 'default', 'admin'), ('lib_team', 'second', 'reader')`)
	s.in(s.db.Owner, "default", "lib_team", func(tx *sql.Tx) {
		s.write(tx,
			`INSERT INTO documents
			   (library_id, doc_id, title, format, source_path, sha256, status)
			 VALUES ('lib_team', 'team-manual', 'Team Manual', 'pdf', '/team.pdf', 'def', 'ready')`)
		s.write(tx,
			`INSERT INTO chunks (library_id, doc_id, ordinal, heading_path, text)
			 VALUES ('lib_team', 'team-manual', 0, '1. Rigging', 'the truss load table')`)
	})
}

// in runs fn in a transaction where user works in library, which is what a
// request for someone else's library will do once one can be named.
func (s *IsolationSuite) in(db *sql.DB, user, library string, fn func(*sql.Tx)) {
	tx := s.begin(db, user, library)
	defer func() { s.NoError(pgsession.Rollback(tx)) }()
	fn(tx)
	s.Require().NoError(tx.Commit())
}

// begin opens a transaction in which user works in library.
func (s *IsolationSuite) begin(db *sql.DB, user, library string) *sql.Tx {
	tx, err := db.BeginTx(s.T().Context(), nil)
	s.Require().NoError(err)
	_, err = tx.ExecContext(s.T().Context(),
		"SELECT set_config('app.user_id', $1, true), set_config('app.library_id', $2, true)",
		user, library)
	s.Require().NoError(err)
	return tx
}

func (s *IsolationSuite) TestAReaderSeesASharedLibrary() {
	s.shared()
	s.in(s.db.App, "second", "lib_team", func(tx *sql.Tx) {
		var text string
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT text FROM chunks`).Scan(&text))
		s.Contains(text, "truss load")
	})
}

func (s *IsolationSuite) TestAReaderCannotChangeASharedLibrary() {
	s.shared()
	s.in(s.db.App, "second", "lib_team", func(tx *sql.Tx) {
		for _, statement := range []string{
			`UPDATE documents SET title = 'renamed'`,
			`DELETE FROM chunks`,
		} {
			result, err := tx.ExecContext(s.T().Context(), statement)
			s.Require().NoError(err, statement)
			affected, err := result.RowsAffected()
			s.Require().NoError(err)
			s.Zero(affected, statement)
		}
	})
	// A refused insert aborts its transaction, so it gets one of its own.
	tx := s.begin(s.db.App, "second", "lib_team")
	defer func() { s.NoError(pgsession.Rollback(tx)) }()
	_, err := tx.ExecContext(s.T().Context(),
		`INSERT INTO documents (library_id, doc_id, title, format, source_path, sha256, status)
		 VALUES ('lib_team', 'smuggled', 'T', 'pdf', '/x', 'abc', 'ready')`)
	s.Require().Error(err, "a reader inserted a document")
}

func (s *IsolationSuite) TestAnEditorChangesASharedLibrary() {
	s.shared()
	s.exec(
		`UPDATE library_grants SET role = 'editor' WHERE library_id = 'lib_team' AND user_id = 'second'`,
	)
	s.in(s.db.App, "second", "lib_team", func(tx *sql.Tx) {
		result, err := tx.ExecContext(s.T().Context(), `UPDATE documents SET title = 'Renamed'`)
		s.Require().NoError(err)
		affected, err := result.RowsAffected()
		s.Require().NoError(err)
		s.Equal(int64(1), affected)
	})
}

func (s *IsolationSuite) TestNamingALibraryWithoutAGrantSeesNothing() {
	// second holds no grant on default's library, so naming it is the
	// mistake the grant check exists for: the wrong library gets nothing.
	s.in(s.db.App, "second", "default", func(tx *sql.Tx) {
		for _, table := range []string{
			"documents", "chunks", "pages", "index_terms", "ingest_jobs",
			"uploads", "upload_links", "responses", "robots",
		} {
			var n int
			s.Require().NoError(tx.QueryRowContext(
				s.T().Context(),
				`SELECT COUNT(*) FROM `+table,
			).Scan(&n), table) //nolint:gosec // a constant from this list
			s.Zero(n, table)
		}
	})
}

func (s *IsolationSuite) TestAGrantReachesOnlyTheLibraryATransactionNames() {
	// second reads two libraries, and a transaction in one of them sees
	// that one alone: the scalar is what lets Postgres prune to a partition.
	s.shared()
	s.in(s.db.App, "second", "lib_team", func(tx *sql.Tx) {
		var docs []string
		rows, err := tx.QueryContext(s.T().Context(), `SELECT DISTINCT doc_id FROM chunks`)
		s.Require().NoError(err)
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var doc string
			s.Require().NoError(rows.Scan(&doc))
			docs = append(docs, doc)
		}
		s.Require().NoError(rows.Err())
		s.Equal([]string{"team-manual"}, docs)
	})
}

func (s *IsolationSuite) TestTheRestrictedRoleCannotReadGrants() {
	// A user learns their own grants through app_libraries and nothing about
	// anyone else's.
	_, err := s.db.App.ExecContext(s.T().Context(), `SELECT * FROM library_grants`)
	s.Require().Error(err)
}
