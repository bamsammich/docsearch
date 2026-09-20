package schema_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	_ "modernc.org/sqlite" // the driver the index is written with

	"github.com/bamsammich/docsearch/internal/schema"
)

// SchemaSuite covers what a migration will and will not stamp. Every case
// runs against a real database, because what a migration is for is the shape
// of one.
type SchemaSuite struct {
	suite.Suite
	db *sql.DB
}

func TestSchema(t *testing.T) { suite.Run(t, new(SchemaSuite)) }

func (s *SchemaSuite) SetupTest() {
	db, err := sql.Open("sqlite", filepath.Join(s.T().TempDir(), "index.db"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(db.Close()) })
	s.db = db
}

// recorded is the version the database carries, and whether it carries one.
func (s *SchemaSuite) recorded() (int, bool) {
	version, ok, err := schema.Recorded(s.T().Context(), s.db)
	s.Require().NoError(err)
	return version, ok
}

// exec runs one statement against the index.
func (s *SchemaSuite) exec(statement string, args ...any) {
	_, err := s.db.ExecContext(s.T().Context(), statement, args...)
	s.Require().NoError(err)
}

func (s *SchemaSuite) TestCreatingAnIndexRecordsItsVersion() {
	// An index nothing stamped is refused by every command that opens it,
	// which is how a Go-built index used to reach the Python CLI.
	s.Require().NoError(schema.Create(s.T().Context(), s.db))

	version, ok := s.recorded()
	s.True(ok)
	s.Equal(schema.Version, version)
	s.Require().NoError(schema.Check(s.T().Context(), s.db))
}

func (s *SchemaSuite) TestAFreshIndexHoldsEveryTableTheGateChecksFor() {
	s.Require().NoError(schema.Create(s.T().Context(), s.db))

	problems, err := schema.Problems(s.T().Context(), s.db)
	s.Require().NoError(err)
	s.Empty(problems)
}

// unstamped is an index the schema built and nothing recorded, which is what
// a database from before the version stamp looks like.
func (s *SchemaSuite) unstamped() {
	s.Require().NoError(schema.Create(s.T().Context(), s.db))
	s.exec(`DELETE FROM schema_version`)
	s.exec(`DROP TABLE goose_db_version`)
}

func (s *SchemaSuite) TestAnUnversionedIndexIsOutdatedRatherThanBroken() {
	s.unstamped()

	err := schema.Check(s.T().Context(), s.db)
	var outdated *schema.ErrOutdated
	s.Require().ErrorAs(err, &outdated)
	s.False(outdated.Recorded)
	s.Contains(err.Error(), "unversioned")
	s.Contains(err.Error(), "docsearch migrate")
}

func (s *SchemaSuite) TestMigratingAnUnversionedIndexStampsIt() {
	s.unstamped()

	result, err := schema.Migrate(s.T().Context(), s.db)
	s.Require().NoError(err)
	s.False(result.FromRecorded)
	s.Equal(schema.Version, result.To)
	s.Require().NoError(schema.Check(s.T().Context(), s.db))
}

func (s *SchemaSuite) TestMigratingTwiceChangesNothingTheSecondTime() {
	_, err := schema.Migrate(s.T().Context(), s.db)
	s.Require().NoError(err)

	again, err := schema.Migrate(s.T().Context(), s.db)
	s.Require().NoError(err)
	s.Empty(again.ColumnsAdded)
	s.Equal(schema.Version, again.From)
}

func (s *SchemaSuite) TestAnIndexFromANewerBuildIsRefused() {
	// Nothing here can know what changed, so serving it would be a guess.
	s.Require().NoError(schema.Create(s.T().Context(), s.db))
	s.exec(`UPDATE schema_version SET version = ?`, schema.Version+1)

	var tooNew *schema.ErrTooNew
	s.Require().ErrorAs(schema.Check(s.T().Context(), s.db), &tooNew)
	s.Equal(schema.Version+1, tooNew.Found)

	_, err := schema.Migrate(s.T().Context(), s.db)
	s.Require().ErrorAs(err, &tooNew)
}

func (s *SchemaSuite) TestAColumnAnOlderSchemaLacksIsAddedAndNamed() {
	// CREATE TABLE IF NOT EXISTS leaves an existing table untouched, so a
	// column added after the initial schema needs an explicit backfill.
	s.exec(`CREATE TABLE documents (
		doc_id TEXT PRIMARY KEY, title TEXT NOT NULL, format TEXT NOT NULL,
		source_path TEXT NOT NULL, sha256 TEXT NOT NULL, status TEXT NOT NULL,
		page_count INTEGER, chunk_count INTEGER, ingested_at TEXT)`)

	result, err := schema.Migrate(s.T().Context(), s.db)
	s.Require().NoError(err)
	s.Contains(result.ColumnsAdded, "documents.warnings")
	s.Contains(result.ColumnsAdded, "documents.source_kind")
	s.Require().NoError(schema.Check(s.T().Context(), s.db))
}

func (s *SchemaSuite) TestAnIndexPredatingSectionReferencesIsRefused() {
	// The version 2 change was semantic: index_terms.section held printed
	// page numbers and now holds section numbers. Nothing recovers one from
	// the other without the source documents, and the index is regenerable,
	// so refusing beats stamping a version the data does not match.
	// Built by hand rather than by the schema, because an index that
	// predates the change is one no migration of this build ever touched.
	s.exec(`CREATE TABLE index_terms (doc_id TEXT NOT NULL, term TEXT NOT NULL, page INTEGER)`)

	_, err := schema.Migrate(s.T().Context(), s.db)
	var refused *schema.ErrUnmigratable
	s.Require().ErrorAs(err, &refused)
	s.Contains(err.Error(), "delete it and re-ingest the library")
	s.Contains(err.Error(), "was NOT recorded")

	_, recorded := s.recorded()
	s.False(recorded, "a refused migration records nothing")
}

func (s *SchemaSuite) TestAMissingTableStopsTheStamp() {
	// A stamp is a claim that the database matches the code, and writing it
	// unchecked makes the claim unfalsifiable.
	s.Require().NoError(schema.Create(s.T().Context(), s.db))
	s.exec(`DROP TABLE pages`)
	s.exec(`CREATE VIEW pages_placeholder AS SELECT 1`)

	problems, err := schema.Problems(s.T().Context(), s.db)
	s.Require().NoError(err)
	s.Contains(problems, "table pages is missing")
}

// The number is chosen once. The Python pipeline is still the reference, and
// the server refuses to serve a database it was not built against, so a
// build where the three disagree would refuse sound indexes or serve unsound
// ones.
func TestTheVersionAgreesWithPython(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(root, "python", "docsearch", "db.py"))
	if err != nil {
		t.Fatal(err)
	}
	want := "SCHEMA_VERSION = " + strconv.Itoa(schema.Version)
	if !strings.Contains(string(source), want) {
		t.Errorf("db.py does not hold %q; internal/schema.Version is %d", want, schema.Version)
	}
}

// The schema is one file. The baseline migration wraps it in goose's
// annotations, and a baseline that drifted would have sqlc typing its queries
// against one shape while the migration wrote another.
func TestTheBaselineMigrationMatchesTheSchema(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(root, "python", "docsearch", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile(filepath.Join(
		root, "internal", "schema", "migrations", "00005_baseline.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(baseline), string(original)) {
		t.Error("00005_baseline.sql has drifted from the schema; run `mise run generate`")
	}
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", os.ErrNotExist
		}
		dir = up
	}
}
