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

// up applies every migration.
func (s *MigrationSuite) up() {
	provider, err := schema.ProviderFor(schema.Postgres, s.db)
	s.Require().NoError(err)
	_, err = provider.Up(s.T().Context())
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
	s.up()
	s.library()

	s.Equal(2, s.count(`SELECT COUNT(*) FROM chunks WHERE doc_id = 'guide'`))
	s.Equal(1, s.count(`SELECT COUNT(*) FROM pages WHERE doc_id = 'guide'`))
	s.Equal(1, s.count(`SELECT COUNT(*) FROM index_terms WHERE doc_id = 'guide'`))
	s.Equal(1, s.count(`SELECT COUNT(*) FROM ingest_jobs`))
}

func (s *MigrationSuite) TestBaselineNumbersAChunkWithoutBeingTold() {
	// SQLite filled the key from the rowid. An identity column is what
	// replaces it, and a chunk insert never names an id.
	s.up()
	s.library()

	s.Equal(2, s.count(`SELECT COUNT(DISTINCT id) FROM chunks`))
	s.Equal(0, s.count(`SELECT COUNT(*) FROM chunks WHERE id IS NULL`))
}

func (s *MigrationSuite) TestBaselineRefusesAChunkWithNoDocument() {
	// The foreign key is what keeps a chunk from outliving its document,
	// which is the integrity the verification report assumes.
	s.up()

	_, err := s.db.ExecContext(s.T().Context(),
		`INSERT INTO chunks (doc_id, ordinal, heading_path, text)
		 VALUES ('absent', 0, 'Nowhere', 'orphan')`)
	s.Require().Error(err)
}

func (s *MigrationSuite) TestBaselineOrdersTimestampsByTimeRatherThanByText() {
	// The timestamps were TEXT on SQLite, where '2026-9-1' sorts after
	// '2026-10-1'. timestamptz is what makes a lease comparison mean what it
	// reads as.
	s.up()
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
	s.up()
	s.library()

	s.down()

	for _, name := range []string{
		"documents", "chunks", "pages", "index_terms", "ingest_jobs", "schema_version",
	} {
		s.False(s.tableExists(name), name)
	}
}

func (s *MigrationSuite) TestBaselineGoesDownAndUpAgain() {
	s.up()
	s.library()
	s.down()
	s.up()

	s.True(s.tableExists("documents"))
	s.Equal(0, s.count(`SELECT COUNT(*) FROM documents`),
		"the rows went with the tables; an index is regenerable")

	s.library()
	s.Equal(2, s.count(`SELECT COUNT(*) FROM chunks`))
}
