package schema_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
	_ "modernc.org/sqlite" // the driver an index is written with

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
// SQLite is embedded, so the real database is a file. When Postgres arrives
// in phase 04 the same cases run against a container, which is what
// testcontainers is for.
type MigrationSuite struct {
	suite.Suite
	db *sql.DB
}

func TestMigrations(t *testing.T) { suite.Run(t, new(MigrationSuite)) }

func (s *MigrationSuite) SetupTest() {
	db, err := sql.Open("sqlite", filepath.Join(s.T().TempDir(), "index.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })
	s.db = db
}

// up applies every migration.
func (s *MigrationSuite) up() {
	provider, err := schema.Provider(s.db)
	s.Require().NoError(err)
	_, err = provider.Up(s.T().Context())
	s.Require().NoError(err)
}

// down rolls the most recent migration back.
func (s *MigrationSuite) down() {
	provider, err := schema.Provider(s.db)
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
		`SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table','view') AND name = ?`,
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
		VALUES ('/library/guide.md', 'done', datetime('now'), datetime('now'))`)
}

// -- 00005_baseline --------------------------------------------------------

func (s *MigrationSuite) TestBaselineUpHoldsADocumentAndIndexesItsText() {
	s.up()
	s.library()

	s.Equal(2, s.count(`SELECT COUNT(*) FROM chunks WHERE doc_id = 'guide'`))
	s.Equal(1, s.count(`SELECT COUNT(*) FROM pages WHERE doc_id = 'guide'`))
	s.Equal(1, s.count(`SELECT COUNT(*) FROM index_terms WHERE doc_id = 'guide'`))
	s.Equal(1, s.count(`SELECT COUNT(*) FROM ingest_jobs`))

	// The trigger, not the insert, is what fills the full-text index, so a
	// baseline that created the tables without them would pass every other
	// check and return nothing to any search.
	s.Equal(1, s.count(`SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'installer'`))
}

func (s *MigrationSuite) TestBaselineUpKeepsTheFullTextIndexWithItsChunks() {
	// The AFTER DELETE trigger is what clears chunks_fts. Without it a
	// deleted chunk keeps matching, and a search cites a chunk that is gone.
	s.up()
	s.library()

	s.exec(`DELETE FROM chunks WHERE ordinal = 0`)
	s.Equal(0, s.count(`SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'installer'`))
	s.Equal(1, s.count(`SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'description'`))
}

func (s *MigrationSuite) TestBaselineDownRemovesEverythingItMade() {
	s.up()
	s.library()

	s.down()

	for _, name := range []string{
		"documents", "chunks", "chunks_fts", "pages", "index_terms",
		"ingest_jobs", "schema_version",
	} {
		s.False(s.tableExists(name), name)
	}
}

func (s *MigrationSuite) TestBaselineDownLeavesNoTriggerBehind() {
	// A trigger outliving the table it writes to breaks the next insert into
	// chunks, which is the table a re-applied baseline creates first.
	s.up()
	s.down()

	var triggers int
	err := s.db.QueryRowContext(s.T().Context(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger'`).Scan(&triggers)
	s.Require().NoError(err)
	s.Zero(triggers)
}

func (s *MigrationSuite) TestBaselineGoesDownAndUpAgain() {
	s.up()
	s.library()
	s.down()
	s.up()

	s.True(s.tableExists("documents"))
	s.Equal(0, s.count(`SELECT COUNT(*) FROM documents`),
		"the rows went with the tables; an index is regenerable")

	// The rebuilt schema still works, triggers and all.
	s.library()
	s.Equal(1, s.count(`SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'installer'`))
}

func (s *MigrationSuite) TestBaselineAppliedTwiceChangesNothing() {
	// An index the Python pipeline created carries no goose record, so the
	// first time goose meets one it applies the baseline over a schema that
	// is already there.
	s.up()
	s.library()
	s.exec(`DROP TABLE goose_db_version`)

	s.up()

	s.Equal(2, s.count(`SELECT COUNT(*) FROM chunks WHERE doc_id = 'guide'`),
		"the rows survive a baseline applied over them")
	s.Equal(1, s.count(`SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'installer'`))
}
