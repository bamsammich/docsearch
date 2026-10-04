package schema_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

// MigrationSuite runs every migration against a real database, both ways,
// over rows the migration touches.
//
// Every migration gets a case here. A migration is the one piece of code
// that runs against data nobody can re-create, so asserting it in the
// abstract asserts nothing: what matters is what happens to rows that are
// already there.
//
// The database is a container, reached as the role that owns the schema
// rather than as a superuser, so a migration reaching for a privilege a
// deployment lacks fails here instead of on the cluster.
type MigrationSuite struct {
	suite.Suite
	db *sql.DB
}

func TestMigrations(t *testing.T) { suite.Run(t, new(MigrationSuite)) }

func (s *MigrationSuite) SetupTest() {
	s.db = pgtest.Start(s.T()).Owner
}

// upTo applies the migrations through one version, for a case that needs the
// shape a later migration changes.
func (s *MigrationSuite) upTo(version int) {
	provider, err := schema.ProviderFor(schema.Postgres, s.db)
	s.Require().NoError(err)
	_, err = provider.UpTo(s.T().Context(), int64(version))
	s.Require().NoError(err)
}

// down rolls the most recent migration back.
func (s *MigrationSuite) down() {
	provider, err := schema.ProviderFor(schema.Postgres, s.db)
	s.Require().NoError(err)
	_, err = provider.Down(s.T().Context())
	s.Require().NoError(err)
}

func (s *MigrationSuite) exec(statement string, args ...any) {
	_, err := s.db.ExecContext(s.T().Context(), statement, args...)
	s.Require().NoError(err)
}

// count reads one number, and fails the test where the query cannot run.
func (s *MigrationSuite) count(query string, args ...any) int {
	var n int
	s.Require().NoError(s.db.QueryRowContext(s.T().Context(), query, args...).Scan(&n))
	return n
}

// tableExists reports whether the database holds a table or view by name.
func (s *MigrationSuite) tableExists(name string) bool {
	var found int
	err := s.db.QueryRowContext(s.T().Context(),
		`SELECT COUNT(*) FROM information_schema.tables
		  WHERE table_schema = current_schema() AND table_name = $1`,
		name).Scan(&found)
	s.Require().NoError(err)
	return found == 1
}

// library fills the index with the rows every table holds, so a migration is
// exercised against data rather than against an empty shape.
func (s *MigrationSuite) library() {
	s.exec(`INSERT INTO documents
		(doc_id, title, format, source_path, source_kind, sha256, status, chunk_count)
		VALUES ('guide', 'Operator Guide', 'markdown', '/library/guide.md', 'file',
		        'abc123', 'ready', 2)`)
	s.exec(`INSERT INTO chunks
		(doc_id, ordinal, section, heading_path, text, kind, image_count, url, fragment)
		VALUES ('guide', 0, '1', 'Operator Guide > Install',
		        'Unpack the archive and run the installer.', 'prose', 0, NULL, NULL)`)
	s.exec(`INSERT INTO chunks
		(doc_id, ordinal, section, heading_path, text, kind, image_count, url, fragment)
		VALUES ('guide', 1, '2', 'Operator Guide > Usage',
		        'Point the tool at a description file.', 'prose', 0,
		        'https://example.com/docs/usage', 'running')`)
	s.exec(`INSERT INTO pages (doc_id, page, text) VALUES ('guide', 1, 'first page')`)
	s.exec(`INSERT INTO index_terms (doc_id, term, section)
		VALUES ('guide', 'installer', '1')`)
	s.exec(`INSERT INTO ingest_jobs (source_path, status, created_at, updated_at)
		VALUES ('/library/guide.md', 'done', now(), now())`)
}

// -- 00006_postgres_baseline ----------------------------------------------

func (s *MigrationSuite) TestBaselineUpHoldsADocumentAndEverythingThatHangsOffIt() {
	s.upTo(5)
	s.library()

	s.Equal(2, s.count(`SELECT COUNT(*) FROM chunks WHERE doc_id = 'guide'`))
	s.Equal(1, s.count(`SELECT COUNT(*) FROM pages WHERE doc_id = 'guide'`))
	s.Equal(1, s.count(`SELECT COUNT(*) FROM index_terms WHERE doc_id = 'guide'`))
	s.Equal(1, s.count(`SELECT COUNT(*) FROM ingest_jobs`))
}

func (s *MigrationSuite) TestBaselineNumbersAChunkWithoutBeingTold() {
	// SQLite filled the key from the rowid. An identity column is what
	// replaces it, and a chunk insert never names an id.
	s.upTo(5)
	s.library()

	s.Equal(2, s.count(`SELECT COUNT(DISTINCT id) FROM chunks`))
	s.Equal(0, s.count(`SELECT COUNT(*) FROM chunks WHERE id IS NULL`))
}

func (s *MigrationSuite) TestBaselineRefusesAChunkWithNoDocument() {
	// The foreign key is what keeps a chunk from outliving its document,
	// which is the integrity the verification report assumes.
	s.upTo(5)

	_, err := s.db.ExecContext(s.T().Context(),
		`INSERT INTO chunks (doc_id, ordinal, heading_path, text)
		 VALUES ('absent', 0, 'Nowhere', 'orphan')`)
	s.Require().Error(err)
}

func (s *MigrationSuite) TestBaselineOrdersTimestampsByTimeRatherThanByText() {
	// The timestamps were TEXT on SQLite, where '2026-9-1' sorts after
	// '2026-10-1'. timestamptz is what makes a lease comparison mean what it
	// reads as.
	s.upTo(5)
	s.exec(`INSERT INTO ingest_jobs (source_path, status, created_at, updated_at)
		VALUES ('/library/early.md', 'done', '2026-09-01T00:00:00Z', now())`)
	s.exec(`INSERT INTO ingest_jobs (source_path, status, created_at, updated_at)
		VALUES ('/library/late.md', 'done', '2026-10-01T00:00:00Z', now())`)

	var first string
	s.Require().NoError(s.db.QueryRowContext(s.T().Context(),
		`SELECT source_path FROM ingest_jobs ORDER BY created_at DESC LIMIT 1`).Scan(&first))
	s.Equal("/library/late.md", first)
}

func (s *MigrationSuite) TestBaselineDownRemovesEverythingItMade() {
	s.upTo(5)
	s.library()

	s.down()

	for _, name := range []string{
		"documents", "chunks", "pages", "index_terms", "ingest_jobs", "schema_version",
	} {
		s.False(s.tableExists(name), name)
	}
}

func (s *MigrationSuite) TestBaselineGoesDownAndUpAgain() {
	s.upTo(5)
	s.library()
	s.down()
	s.upTo(5)

	s.True(s.tableExists("documents"))
	s.Equal(0, s.count(`SELECT COUNT(*) FROM documents`),
		"the rows went with the tables; an index is regenerable")

	s.library()
	s.Equal(2, s.count(`SELECT COUNT(*) FROM chunks`))
}

// ownedLibrary is the same rows, written after migration 6 has given every
// row an owner: FORCE binds even the owner, so a seed names its user.
func (s *MigrationSuite) ownedLibrary(userID string) {
	pgtest.WriteAs(s.T(), s.db, userID, func(tx *sql.Tx) {
		for _, statement := range []string{
			`INSERT INTO documents
			   (user_id, doc_id, title, format, source_path, source_kind, sha256,
			    status, chunk_count)
			 VALUES ($1, 'guide', 'Operator Guide', 'markdown', '/library/guide.md',
			         'file', 'abc123', 'ready', 2)`,
			`INSERT INTO chunks (user_id, doc_id, ordinal, section, heading_path, text)
			 VALUES ($1, 'guide', 0, '1', 'Operator Guide > Install',
			         'Unpack the archive and run the installer.')`,
			`INSERT INTO chunks (user_id, doc_id, ordinal, section, heading_path, text)
			 VALUES ($1, 'guide', 1, '2', 'Operator Guide > Usage',
			         'Point the tool at a description file.')`,
		} {
			_, err := tx.ExecContext(s.T().Context(), statement, userID)
			s.Require().NoError(err)
		}
	})
}

// -- 00006_users ----------------------------------------------------------

func (s *MigrationSuite) TestUsersUpGivesTheRowsAlreadyThereAnOwner() {
	// The rows exist before the migration and must come out of it belonging
	// to the one user, which is what makes a v1 library survive the move.
	s.upTo(5)
	s.library()

	s.upTo(6)

	pgtest.ReadAs(s.T(), s.db, "default", func(tx *sql.Tx) {
		var chunks, documents int
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT COUNT(*) FROM chunks`).Scan(&chunks))
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT COUNT(*) FROM documents`).Scan(&documents))
		s.Equal(2, chunks)
		s.Equal(1, documents)
	})
}

func (s *MigrationSuite) TestUsersUpMovesChunksIntoAPartition() {
	s.upTo(5)
	s.library()

	s.upTo(6)

	pgtest.ReadAs(s.T(), s.db, "default", func(tx *sql.Tx) {
		var partition string
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT DISTINCT tableoid::regclass::text FROM chunks`).Scan(&partition))
		s.Equal("chunks_default", partition)
	})
}

func (s *MigrationSuite) TestUsersUpKeepsNumberingChunksWhereItLeftOff() {
	// The rows move through a copy, so the identity column has to be told
	// where they got to or the next chunk collides with one that came across.
	s.upTo(5)
	s.library()
	s.upTo(6)

	pgtest.WriteAs(s.T(), s.db, "default", func(tx *sql.Tx) {
		_, err := tx.ExecContext(s.T().Context(),
			`INSERT INTO chunks (user_id, doc_id, ordinal, heading_path, text)
			 VALUES ('default', 'guide', 2, 'Operator Guide > Later', 'a third chunk')`)
		s.Require().NoError(err)
	})

	pgtest.ReadAs(s.T(), s.db, "default", func(tx *sql.Tx) {
		var distinct, total int
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT COUNT(DISTINCT id), COUNT(*) FROM chunks`).Scan(&distinct, &total))
		s.Equal(3, total)
		s.Equal(3, distinct, "a new chunk took an id no moved chunk already had")
	})
}

func (s *MigrationSuite) TestUsersDownReturnsToOneLibrary() {
	s.upTo(5)
	s.library()
	s.upTo(6)

	s.down()

	s.False(s.tableExists("users"))
	s.Equal(2, s.count(`SELECT COUNT(*) FROM chunks`),
		"the one user's chunks come back, and read without naming a user")
	s.Equal(0, s.count(
		`SELECT COUNT(*) FROM information_schema.columns
		  WHERE table_name = 'documents' AND column_name = 'user_id'`))
}

func (s *MigrationSuite) TestUsersGoesDownAndUpAgain() {
	s.upTo(5)
	s.library()
	s.upTo(6)
	s.down()
	s.upTo(6)

	pgtest.ReadAs(s.T(), s.db, "default", func(tx *sql.Tx) {
		var chunks int
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT COUNT(*) FROM chunks`).Scan(&chunks))
		s.Equal(2, chunks, "the rows survive a round trip through both shapes")
	})
}

// -- 00007_search ---------------------------------------------------------

func (s *MigrationSuite) TestSearchUpGivesEveryPartitionItsOwnIndex() {
	// Declared on the parent, so each user's statistics cover that user's
	// rows: BM25 reads its inverse document frequency from the index it
	// searches.
	s.upTo(6)
	s.exec(`INSERT INTO users (user_id) VALUES ('second')`)
	s.exec(`CREATE TABLE chunks_second PARTITION OF chunks FOR VALUES IN ('second')`)

	s.upTo(7)

	s.Equal(2, s.count(
		`SELECT COUNT(*) FROM pg_index
		   JOIN pg_class ON pg_class.oid = pg_index.indexrelid
		  WHERE pg_get_indexdef(pg_index.indexrelid) LIKE '%bm25%'
		    AND pg_class.relname <> 'chunks_bm25'`),
		"one index per partition, beside the parent's own")
}

func (s *MigrationSuite) TestSearchFindsAChunkByItsHeadingAndBody() {
	s.upTo(6)
	s.ownedLibrary("default")

	s.upTo(7)

	pgtest.ReadAs(s.T(), s.db, "default", func(tx *sql.Tx) {
		var heading string
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT heading_path FROM chunks
			  ORDER BY (heading_path || ' ' || heading_path || ' ' || text)
			          <@> to_bm25query('installer', 'chunks_bm25')
			  LIMIT 1`).Scan(&heading))
		s.Contains(heading, "Install")
	})
}

func (s *MigrationSuite) TestSearchReturnsOnlyWhatMatches() {
	// Where the index drives the query, the operator yields matching rows
	// alone, as FTS5's MATCH did. Where it does not, which Postgres chooses
	// for a table small enough to scan, the score is computed standalone for
	// every row and non-matches come back at zero. A fresh library is
	// exactly that small, so the store drops non-matches from the candidates
	// the limit already bounded, and the answer is the same under either
	// plan.
	s.upTo(6)
	s.ownedLibrary("default")
	s.upTo(7)

	pgtest.ReadAs(s.T(), s.db, "default", func(tx *sql.Tx) {
		var found int
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT COUNT(*) FROM (
			   SELECT (heading_path || ' ' || heading_path || ' ' || text)
			            <@> to_bm25query('installer', 'chunks_bm25') AS score
			     FROM chunks
			    ORDER BY (heading_path || ' ' || heading_path || ' ' || text)
			            <@> to_bm25query('installer', 'chunks_bm25')
			    LIMIT 80) candidates
			  WHERE score < 0`,
		).Scan(&found))
		s.Equal(1, found, "one chunk mentions the installer")
	})
}

func (s *MigrationSuite) TestSearchDownLeavesTheChunks() {
	s.upTo(6)
	s.ownedLibrary("default")
	s.upTo(7)

	s.down()

	pgtest.ReadAs(s.T(), s.db, "default", func(tx *sql.Tx) {
		var chunks int
		s.Require().NoError(tx.QueryRowContext(s.T().Context(),
			`SELECT COUNT(*) FROM chunks`).Scan(&chunks))
		s.Equal(2, chunks, "an index is regenerable; the rows are not")
	})
}
