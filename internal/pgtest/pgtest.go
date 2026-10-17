// Package pgtest starts the Postgres a test needs, shaped the way a
// deployment is shaped.
//
// Two things make the shape matter more than convenience would suggest.
// testcontainers hands back a superuser, and a test that used it would be
// more privileged than anything docsearch ever runs as: `CREATE EXTENSION`
// would succeed where CloudNativePG's app user cannot, and the migration
// that has to survive a missing extension would never be exercised. Row-level
// security is the other: FORCE binds the table owner too, so a test running
// as a superuser proves nothing about a policy.
//
// So the helper does what a cluster does, once, as the superuser, and then
// hands back the two roles docsearch actually uses: an owner that migrations
// run as, and a restricted role that requests run as, owning nothing and
// lacking BYPASSRLS.
package pgtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the driver the server and worker use
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// Image carries PostgreSQL 17 with pg_textsearch already preloaded, which
	// is why nothing here builds one. The versions are the ones
	// docs/research/postgres-spike.md measured.
	Image = "timescale/timescaledb-ha:pg17"

	// Extension is what search is built on, and what a cluster installs
	// before docsearch connects.
	Extension = "pg_textsearch"

	// OwnerRole runs migrations and owns every table. NOSUPERUSER is the
	// point: a migration must not be able to do what a deployment cannot.
	OwnerRole = "docsearch_owner"
	// AppRole runs requests. It owns nothing, so row-level security applies
	// to it the way it applies in a deployment.
	AppRole = "docsearch_app"

	password = "docsearch-test"
	database = "docsearch"

	// startupTimeout is generous because the first run of a suite pulls the
	// image.
	startupTimeout = 3 * time.Minute
)

// DB is one database, and the two ways into it.
type DB struct {
	// container is kept so a caller can run a tool the image carries, which
	// is how the schema snapshot reaches pg_dump.
	container testcontainers.Container
	// Owner is connected as the role that owns the schema.
	Owner *sql.DB
	// App is connected as the role a request uses.
	App *sql.DB
	// OwnerDSN and AppDSN open further connections, for a test that needs
	// its own pool.
	OwnerDSN string
	AppDSN   string
}

// TestingT is the part of *testing.T these helpers use.
//
// An interface rather than the type, because the end-to-end suite runs under
// ginkgo, and GinkgoT cannot satisfy testing.TB: the interface has an
// unexported method only the standard library can implement.
type TestingT interface {
	Helper()
	Context() context.Context
	Cleanup(func())
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
}

// Start brings up a database with the extension installed and both roles
// created, and closes everything when the test ends.
//
// One container per call. Sharing one across a suite would let a leftover
// table from one case decide another, and the image starts in about two
// seconds.
func Start(t TestingT) *DB {
	t.Helper()
	return start(t, true)
}

// StartWithoutExtension is the same database with the extension left out,
// for the tests that prove docsearch says which extension is missing rather
// than failing on the first query that needs it.
func StartWithoutExtension(t TestingT) *DB {
	t.Helper()
	return start(t, false)
}

func start(t TestingT, withExtension bool) *DB {
	t.Helper()
	ctx := context.Background()

	container, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("cluster_superuser"),
		postgres.WithPassword(password),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(startupTimeout)),
	)
	if err != nil {
		t.Fatalf("start %s: %v", Image, err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("could not remove the container: %v", err)
		}
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}
	dsn := func(role, name string) string {
		return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
			role, password, host, port.Port(), name)
	}

	provision(t,
		dsn("cluster_superuser", "postgres"),
		dsn("cluster_superuser", database),
		withExtension)

	db := &DB{OwnerDSN: dsn(OwnerRole, database), AppDSN: dsn(AppRole, database)}
	db.container = container
	db.Owner = open(t, db.OwnerDSN)
	db.App = open(t, db.AppDSN)
	return db
}

// provision is the cluster's half: the roles, the database and the
// extension, none of which docsearch has the privileges to create.
func provision(t TestingT, superuserDSN, databaseDSN string, withExtension bool) {
	t.Helper()
	cluster := open(t, superuserDSN)
	for _, statement := range []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS`,
			OwnerRole, password),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS`,
			AppRole, password),
		fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, database, OwnerRole),
	} {
		if _, err := cluster.Exec(statement); err != nil {
			t.Fatalf("provision: %v", err)
		}
	}

	inside := open(t, databaseDSN)
	// The app role reads and writes what the owner creates, so it needs the
	// schema. A cluster grants this once; row-level security is what decides
	// which rows it then sees.
	if _, err := inside.Exec(
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, AppRole)); err != nil {
		t.Fatalf("grant schema usage: %v", err)
	}
	if !withExtension {
		return
	}
	if _, err := inside.Exec(`CREATE EXTENSION ` + Extension); err != nil {
		t.Fatalf("create %s: %v", Extension, err)
	}
}

func open(t TestingT, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db
}

// ReadAs runs fn in a transaction that names the user, then rolls back.
//
// The default for anything that is not a seed. FORCE ROW LEVEL SECURITY
// binds the table owner too, so even a read as the owner sees nothing until
// a user is named. Rolling back is what lets a case assert that a statement
// was refused: a refused statement aborts its transaction, and a commit
// afterwards would fail for that reason rather than the one under test.
func ReadAs(t TestingT, db *sql.DB, userID string, fn func(tx *sql.Tx)) {
	t.Helper()
	tx := begin(t, db, userID)
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("roll back: %v", err)
		}
	}()
	fn(tx)
}

// WriteAs runs fn in a transaction that names the user, then commits, so
// what it wrote is there for the rest of the case.
func WriteAs(t TestingT, db *sql.DB, userID string, fn func(tx *sql.Tx)) {
	t.Helper()
	tx := begin(t, db, userID)
	committed := false
	defer func() {
		if committed {
			return
		}
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("roll back: %v", err)
		}
	}()
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	committed = true
}

// begin opens a transaction and names the user it belongs to, which is what
// every request does before it reads or writes anything.
func begin(t TestingT, db *sql.DB, userID string) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// SET LOCAL takes no placeholder, and the identifier comes from the test
	// rather than from a request.
	if _, err := tx.ExecContext(t.Context(),
		fmt.Sprintf("SET LOCAL app.user_id = %s", quote(userID))); err != nil {
		t.Fatalf("set the user: %v", err)
	}
	return tx
}

// quote wraps a literal the way Postgres does, doubling any quote inside it.
func quote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += "'"
		}
		out += string(r)
	}
	return out + "'"
}

// Run executes a command inside the container and returns what it printed.
//
// For the tools the image carries and docsearch does not reimplement, pg_dump
// above all: the schema snapshot is Postgres describing its own shape, which
// is worth more than a renderer of our own reading the catalog.
func (db *DB) Run(ctx context.Context, command ...string) (string, error) {
	// Multiplexed, or Docker's stream framing arrives interleaved with the
	// output as binary headers.
	code, output, err := db.container.Exec(ctx, command, exec.Multiplexed())
	if err != nil {
		return "", fmt.Errorf("run %s: %w", command[0], err)
	}
	printed, err := io.ReadAll(output)
	if err != nil {
		return "", fmt.Errorf("read what %s printed: %w", command[0], err)
	}
	if code != 0 {
		return "", fmt.Errorf("%s exited %d: %s", command[0], code, printed)
	}
	return string(printed), nil
}
