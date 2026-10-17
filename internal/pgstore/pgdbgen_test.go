package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

// Every generated query is executed against a real database.
//
// Compilation proves nothing about a generated query: sqlc emits the SQL as a
// string constant and the argument list separately, so the two can disagree
// and still build. That is not hypothetical -- `BETWEEN ? AND ?` kept its
// placeholders in the emitted SQL while sqlc counted no parameters for them,
// and the generated call passed one argument for a statement wanting three.
// Only running the statement catches it.
//
// Every method is called by reflection with zero-valued arguments, so a query
// added to internal/pgstore/query is covered the day it is generated. The
// SQLite version of this test listed each query by hand and asserted the
// count, which went stale every time someone added one.
//
// What a zero-valued call proves is narrow and exactly the point. The rows
// are meaningless and most statements match nothing or violate a constraint;
// the assertion is that Postgres understood the statement and was handed the
// arguments it asked for.
func TestEveryGeneratedQueryExecutes(t *testing.T) {
	pg := pgtest.Start(t)
	if err := schema.Create(t.Context(), pg.Owner); err != nil {
		t.Fatal(err)
	}

	queries := reflect.TypeFor[*pgdbgen.Queries]()
	ran := 0
	for i := range queries.NumMethod() {
		method := queries.Method(i)
		if !takesContext(method.Type) {
			// WithTx, which hands back another Queries rather than running
			// anything.
			continue
		}
		ran++
		t.Run(method.Name, func(t *testing.T) {
			runGenerated(t, pg, method)
		})
	}
	if ran == 0 {
		t.Fatal("no generated queries were found to run")
	}
}

// takesContext reports whether a method is a generated query: one that takes
// a context first, as every one of them does.
func takesContext(signature reflect.Type) bool {
	// Index 0 is the receiver.
	return signature.NumIn() >= 2 &&
		signature.In(1) == reflect.TypeFor[context.Context]()
}

// runGenerated calls one query inside a transaction that names a user and is
// rolled back, so a statement that does write leaves nothing behind.
func runGenerated(t *testing.T, pg *pgtest.DB, method reflect.Method) {
	t.Helper()
	pgtest.ReadAs(t, pg.Owner, "default", func(tx *sql.Tx) {
		args := []reflect.Value{
			reflect.ValueOf(pgdbgen.New(pg.Owner).WithTx(tx)),
			reflect.ValueOf(t.Context()),
		}
		for i := 2; i < method.Type.NumIn(); i++ {
			args = append(args, reflect.New(method.Type.In(i)).Elem())
		}
		out := method.Func.Call(args)
		if err, ok := out[len(out)-1].Interface().(error); ok && err != nil {
			requireDatabaseAnswer(t, method.Name, err)
		}
	})
}

// requireDatabaseAnswer passes an error the database decided on and fails one
// the driver raised.
//
// A constraint violation, an undefined row, a type mismatch in a zero value:
// all of those are the database answering a statement it understood, which is
// all this test asks for. An argument count that disagrees with the SQL never
// reaches the database, so it arrives as a driver error instead, and that is
// the defect generation introduces.
func requireDatabaseAnswer(t *testing.T, name string, err error) {
	t.Helper()
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	var fromPostgres *pgconn.PgError
	if !errors.As(err, &fromPostgres) {
		t.Errorf("%s: the database never saw this statement: %v", name, err)
	}
}
