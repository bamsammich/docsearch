package pgstore

import (
	"testing"

	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

// Ready is reachable without a token, so it runs before any user is known.
// Neither table it reads carries a policy, which is what makes that sound.
func TestReadyPassesOnAMigratedDatabase(t *testing.T) {
	pg := pgtest.Start(t)
	if err := schema.Create(t.Context(), pg.Owner); err != nil {
		t.Fatal(err)
	}

	store := New(pg.App, "default")
	if err := store.Ready(t.Context()); err != nil {
		t.Errorf("a migrated database is not ready: %v", err)
	}
}

func TestReadyNamesTheTableThatIsMissing(t *testing.T) {
	pg := pgtest.Start(t)
	if err := schema.Create(t.Context(), pg.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Owner.ExecContext(t.Context(), `DROP TABLE pages`); err != nil {
		t.Fatal(err)
	}

	store := New(pg.App, "default")
	err := store.Ready(t.Context())
	if err == nil {
		t.Fatal("a database missing a table reads as ready")
	}
	if got := err.Error(); got != "schema incomplete: pages is missing" {
		t.Errorf("said %q", got)
	}
}
