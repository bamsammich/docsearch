package schema_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgtest"
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
	s.db = pgtest.Start(s.T()).Owner
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

// unstamped is a migrated index carrying no version, which is what a
// migration interrupted between goose's record and the stamp leaves behind.
//
// goose's own record stays. A database that predates goose entirely was a
// SQLite one, and the baseline no longer has to survive being applied over a
// schema that is already there.
func (s *SchemaSuite) unstamped() {
	s.Require().NoError(schema.Create(s.T().Context(), s.db))
	s.exec(`DELETE FROM schema_version`)
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
	s.Equal(schema.Version, again.From)
}

func (s *SchemaSuite) TestAnIndexFromANewerBuildIsRefused() {
	// Nothing here can know what changed, so serving it would be a guess.
	s.Require().NoError(schema.Create(s.T().Context(), s.db))
	s.exec(`UPDATE schema_version SET version = $1`, schema.Version+1)

	var tooNew *schema.ErrTooNew
	s.Require().ErrorAs(schema.Check(s.T().Context(), s.db), &tooNew)
	s.Equal(schema.Version+1, tooNew.Found)

	_, err := schema.Migrate(s.T().Context(), s.db)
	s.Require().ErrorAs(err, &tooNew)
}

func (s *SchemaSuite) TestAMissingTableStopsTheStamp() {
	// A stamp is a claim that the database matches the code, and writing it
	// unchecked makes the claim unfalsifiable.
	s.Require().NoError(schema.Create(s.T().Context(), s.db))
	s.exec(`DROP TABLE pages`)

	problems, err := schema.Problems(s.T().Context(), s.db)
	s.Require().NoError(err)
	s.Contains(problems, "table pages is missing")
}

// Version is the number a reader checks an index against, and the
// migrations are what move a database to it. Both dialects declare the same
// schema, so both must reach the same version: a migration added to one and
// forgotten in the other would leave the two engines disagreeing about what
// version 5, or 6, means.
func TestEveryDialectReachesTheVersion(t *testing.T) {
	dialects, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(dialects) == 0 {
		t.Fatal("no dialects under migrations/")
	}
	for _, dialect := range dialects {
		if !dialect.IsDir() {
			t.Fatalf("migrations/%s is not a dialect directory", dialect.Name())
		}
		if got := highestMigration(t, dialect.Name()); got != schema.Version {
			t.Errorf("the highest %s migration is %d but internal/schema.Version is %d",
				dialect.Name(), got, schema.Version)
		}
	}
}

// highestMigration is the last version one dialect's migrations reach.
func highestMigration(t *testing.T, dialect string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("migrations", dialect))
	if err != nil {
		t.Fatal(err)
	}
	highest := 0
	for _, entry := range entries {
		if n := migrationNumber(t, entry.Name()); n > highest {
			highest = n
		}
	}
	return highest
}

// migrationNumber is the order goose applies a migration in, which its file
// name carries ahead of the first underscore.
func migrationNumber(t *testing.T, name string) int {
	t.Helper()
	digits, _, found := strings.Cut(name, "_")
	if !found {
		t.Fatalf("migration %q is not numbered", name)
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		t.Fatalf("migration %q is not numbered: %v", name, err)
	}
	return n
}

func TestAMigrationNamesTheExtensionTheClusterDidNotInstall(t *testing.T) {
	// Creating it needs a superuser, so docsearch cannot repair this. Saying
	// which extension is missing is the whole of what it can usefully do,
	// and saying it here beats failing on the first query that needs one.
	db := pgtest.StartWithoutExtension(t)

	_, err := schema.Migrate(t.Context(), db.Owner)
	var missing *schema.ErrMissingExtension
	if !errors.As(err, &missing) {
		t.Fatalf("migrate on a database without the extension: %v", err)
	}
	if missing.Name != pgtest.Extension {
		t.Errorf("named %q, want %q", missing.Name, pgtest.Extension)
	}
	if !strings.Contains(err.Error(), "shared_preload_libraries") {
		t.Errorf("the error does not say what to do: %s", err)
	}
}
