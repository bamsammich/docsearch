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
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the driver the server and worker use
	"github.com/testcontainers/testcontainers-go"
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
	// Owner is connected as the role that owns the schema.
	Owner *sql.DB
	// App is connected as the role a request uses.
	App *sql.DB
	// OwnerDSN and AppDSN open further connections, for a test that needs
	// its own pool.
	OwnerDSN string
	AppDSN   string
}

// Start brings up a database with the extension installed and both roles
// created, and closes everything when the test ends.
//
// One container per call. Sharing one across a suite would let a leftover
// table from one case decide another, and the image starts in about two
// seconds.
func Start(t *testing.T) *DB {
	t.Helper()
	return start(t, true)
}

// StartWithoutExtension is the same database with the extension left out,
// for the tests that prove docsearch says which extension is missing rather
// than failing on the first query that needs it.
func StartWithoutExtension(t *testing.T) *DB {
	t.Helper()
	return start(t, false)
}

func start(t *testing.T, withExtension bool) *DB {
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
	db.Owner = open(t, db.OwnerDSN)
	db.App = open(t, db.AppDSN)
	return db
}

// provision is the cluster's half: the roles, the database and the
// extension, none of which docsearch has the privileges to create.
func provision(t *testing.T, superuserDSN, databaseDSN string, withExtension bool) {
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

func open(t *testing.T, dsn string) *sql.DB {
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

// AsUser runs fn in a transaction that names the user first, which is what
// every request does and what any write has to do: FORCE ROW LEVEL SECURITY
// binds the owner as well, so even a seed says who it writes for.
func AsUser(t *testing.T, db *sql.DB, userID string, fn func(tx *sql.Tx)) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Rolled back where fn failed the test and the commit never ran. After a
	// commit the rollback reports a finished transaction, which is the
	// expected answer rather than a failure.
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("roll back: %v", err)
		}
	}()

	// SET LOCAL takes no placeholder, and the user identifier comes from the
	// test rather than from a request.
	if _, err := tx.ExecContext(t.Context(),
		fmt.Sprintf("SET LOCAL app.user_id = %s", quote(userID))); err != nil {
		t.Fatalf("set the user: %v", err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
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
