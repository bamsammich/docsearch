//go:build spike

// Package searchcost measures what an unscoped search costs as one library
// grows, and the statements that could replace one candidate query per
// document.
//
// It runs against a throwaway container, never a live database:
//
//	go test -tags spike -run TestSearchCost -v -timeout 60m ./spike/searchcost/
package searchcost

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bamsammich/docsearch/internal/pgstore"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

const (
	vocabulary    = 20000
	wordsPerChunk = 100
	queriesPerRun = 10
	warmups       = 2
	timedRuns     = 5
	pool          = 32 // pgstore's candidate pool at k=8
)

var sizes = sizesFromEnv()

// sizesFromEnv is the library sizes to measure, 20, 200 and 500 unless
// SEARCHCOST_SIZES lists others.
func sizesFromEnv() []int {
	raw := os.Getenv("SEARCHCOST_SIZES")
	if raw == "" {
		return []int{20, 200, 500}
	}
	var out []int
	for _, f := range strings.Split(raw, ",") {
		var n int
		if _, err := fmt.Sscan(f, &n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// scored is pgstore's scored expression, verbatim.
const scored = `(chunks.heading_path || ' ' || chunks.heading_path || ' ' ||
	chunks.text) <@> to_bm25query($1, 'chunks_bm25')`

// candidate is one row of a document's pool.
type candidate struct {
	doc   string
	id    int64
	score float64
}

func TestSearchCost(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Start(t)
	if _, err := schema.Migrate(ctx, db.Owner); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db.Owner, `CREATE POLICY spike_load ON chunks TO docsearch_owner USING (true) WITH CHECK (true)`)
	exec(t, db.Owner, `CREATE POLICY spike_load ON documents TO docsearch_owner USING (true) WITH CHECK (true)`)
	var version string
	if err := db.Owner.QueryRow(`SELECT extversion FROM pg_extension WHERE extname = 'pg_textsearch'`).Scan(&version); err != nil {
		t.Fatalf("version: %v", err)
	}
	t.Logf("pg_textsearch %s", version)

	db.Owner.SetMaxOpenConns(1)
	exec(t, db.Owner, `SELECT setseed(0.42)`)
	queries := pickQueries()

	// A second account holds its own partition, to show what the parent
	// index scores there.
	exec(t, db.App, `SELECT create_account('other', 'spike', 'other')`)
	load(t, db.Owner, "other", 0, 50)
	exec(t, db.Owner, `VACUUM ANALYZE`)
	leafStats(t, ctx, db.App, "other", queries[0])
	var otherHits int
	other, err := pgstore.New(db.App, "other").Search(ctx, pgstore.SearchParams{Query: queries[0], K: 8})
	if err != nil {
		t.Fatalf("search other: %v", err)
	}
	otherHits = len(other)
	t.Logf("pgstore.Search as 'other' over 50 documents returns %d results", otherHits)

	// The library under test grows inside the default partition, the one
	// pg_textsearch takes the parent index's statistics from.
	const user = "default"
	// Naming a leaf index needs SELECT on its partition, which create_account
	// grants for every partition but this one.
	exec(t, db.Owner, `GRANT SELECT ON chunks_default TO docsearch_app`)
	loaded := 0
	t.Log("== one whole search, ten queries x five runs ==")
	t.Logf("%-5s %-32s %-32s %-32s %-32s %-32s %-32s", "docs",
		"pgstore.Search", "loop (candidate per doc)", "lateral over documents", "one walk + window", "one walk, ids then join",
		"cursor walk, then score")
	for _, docs := range sizes {
		load(t, db.Owner, user, loaded, docs-loaded)
		loaded = docs
		exec(t, db.Owner, `VACUUM ANALYZE`)
		leafStats(t, ctx, db.App, user, queries[0])
		parity(t, ctx, db.App, user, queries)
		explain(t, ctx, db.App, user, queries[0], docs)

		store := pgstore.New(db.App, user)
		today := measure(t, queries, func(q string) error {
			_, err := store.Search(ctx, pgstore.SearchParams{Query: q, K: 8})
			return err
		})
		run := func(f func(context.Context, *sql.Tx, string) ([]candidate, error)) string {
			return measure(t, queries, func(q string) error {
				return inTx(ctx, db.App, user, func(tx *sql.Tx) error {
					_, err := f(ctx, tx, q)
					return err
				})
			})
		}
		t.Logf("%-5d %-32s %-32s %-32s %-32s %-32s %-32s", docs, today,
			run(loop), run(lateral), run(window), run(windowThenJoin), run(cursorWalk))
		took := measure(t, queries[:2], func(q string) error {
			return inTx(ctx, db.App, user, func(tx *sql.Tx) error {
				_, err := prefiltered(ctx, tx, q)
				return err
			})
		})
		t.Logf("docs=%-4d prefiltered loop, two queries: %s", docs, took)
	}
	hold(t)
}

// hold keeps the container up for inspection when SEARCHCOST_HOLD names a
// duration.
func hold(t *testing.T) {
	t.Helper()
	d, err := time.ParseDuration(os.Getenv("SEARCHCOST_HOLD"))
	if err != nil {
		return
	}
	t.Logf("holding the container for %s", d)
	time.Sleep(d)
}

// loop is pgstore today: the ready list, then one candidate query each.
func loop(ctx context.Context, tx *sql.Tx, query string) ([]candidate, error) {
	docs, err := strings1(ctx, tx, `SELECT doc_id FROM documents WHERE status = 'ready' ORDER BY doc_id`)
	if err != nil {
		return nil, err
	}
	var out []candidate
	for _, d := range docs {
		rows, err := collect(ctx, tx, `SELECT * FROM (
			SELECT chunks.doc_id, chunks.id, `+scored+` AS score, chunks.text
			  FROM chunks WHERE chunks.doc_id = $2
			 ORDER BY `+scored+` LIMIT $3) candidates WHERE score < 0 ORDER BY score`, query, d, pool)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// lateral is the loop as one statement.
func lateral(ctx context.Context, tx *sql.Tx, query string) ([]candidate, error) {
	return collect(ctx, tx, `SELECT c.doc_id, c.id, c.score, c.text
		  FROM documents d
		 CROSS JOIN LATERAL (
		   SELECT * FROM (
		     SELECT chunks.doc_id, chunks.id, `+scored+` AS score, chunks.text
		       FROM chunks WHERE chunks.doc_id = d.doc_id
		      ORDER BY `+scored+` LIMIT $2) pooled WHERE score < 0) c
		 WHERE d.status = 'ready'
		 ORDER BY c.doc_id, c.score`, query, pool)
}

// window walks the library's BM25 ordering once, to the end, and keeps each
// document's first pool rows.
func window(ctx context.Context, tx *sql.Tx, query string) ([]candidate, error) {
	return collect(ctx, tx, `SELECT doc_id, id, score, text FROM (
		  SELECT walk.*, row_number() OVER (PARTITION BY doc_id ORDER BY score, id) AS rn
		    FROM (SELECT chunks.doc_id, chunks.id, `+scored+` AS score, chunks.text
		            FROM chunks ORDER BY `+scored+` LIMIT $2) walk
		   WHERE score < 0) ranked
		 WHERE rn <= $3 ORDER BY doc_id, score`, query, walkLimit(ctx, tx), pool)
}

// windowThenJoin is window carrying only keys through the sort, reading text
// for the rows kept.
func windowThenJoin(ctx context.Context, tx *sql.Tx, query string) ([]candidate, error) {
	return collect(ctx, tx, `SELECT ranked.doc_id, ranked.id, ranked.score, chunks.text FROM (
		  SELECT walk.*, row_number() OVER (PARTITION BY doc_id ORDER BY score, id) AS rn
		    FROM (SELECT chunks.library_id, chunks.doc_id, chunks.id, `+scored+` AS score
		            FROM chunks ORDER BY `+scored+` LIMIT $2) walk
		   WHERE score < 0) ranked
		  JOIN chunks ON chunks.library_id = ranked.library_id AND chunks.id = ranked.id
		 WHERE rn <= $3 ORDER BY ranked.doc_id, ranked.score`, query, walkLimit(ctx, tx), pool)
}

// cursorWalk walks the ordering through a cursor, carrying keys only, and
// stops once every ready document holds a full pool. A second statement
// scores the rows kept.
func cursorWalk(ctx context.Context, tx *sql.Tx, query string) ([]candidate, error) {
	ready, err := strings1(ctx, tx, `SELECT doc_id FROM documents WHERE status = 'ready'`)
	if err != nil {
		return nil, err
	}
	open := map[string]int{}
	for _, d := range ready {
		open[d] = 0
	}
	if _, err := tx.ExecContext(ctx, `DECLARE walk NO SCROLL CURSOR FOR
		SELECT chunks.doc_id, chunks.id FROM chunks ORDER BY `+scored, query); err != nil {
		return nil, fmt.Errorf("declare: %w", err)
	}
	var kept []int64
	const batch = 1000
	for len(open) > 0 {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`FETCH FORWARD %d FROM walk`, batch))
		if err != nil {
			return nil, err
		}
		n := 0
		for rows.Next() {
			n++
			var doc string
			var id int64
			if err := rows.Scan(&doc, &id); err != nil {
				_ = rows.Close()
				return nil, err
			}
			have, ok := open[doc]
			if !ok {
				continue
			}
			kept = append(kept, id)
			if have+1 == pool {
				delete(open, doc)
			} else {
				open[doc] = have + 1
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if n < batch {
			break
		}
	}
	if _, err := tx.ExecContext(ctx, `CLOSE walk`); err != nil {
		return nil, err
	}
	return collect(ctx, tx, `SELECT * FROM (
		SELECT chunks.doc_id, chunks.id, `+scored+` AS score, chunks.text
		  FROM chunks WHERE chunks.id = ANY($2)) s WHERE score < 0`, query, kept)
}

// prefiltered reads each document's rows by its btree and scores them
// standalone, the README's pre-filter route.
func prefiltered(ctx context.Context, tx *sql.Tx, query string) ([]candidate, error) {
	docs, err := strings1(ctx, tx, `SELECT doc_id FROM documents WHERE status = 'ready' ORDER BY doc_id`)
	if err != nil {
		return nil, err
	}
	var out []candidate
	for _, d := range docs {
		rows, err := collect(ctx, tx, `SELECT * FROM (
			SELECT chunks.doc_id, chunks.id, `+scored+` AS score, chunks.text
			  FROM chunks WHERE chunks.doc_id = $2 OFFSET 0) s
			 WHERE score < 0 ORDER BY score LIMIT $3`, query, d, pool)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// walkLimit is every chunk the library holds, so the walk ends only when the
// matches do.
func walkLimit(ctx context.Context, tx *sql.Tx) int {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(chunk_count), 0) FROM documents`).Scan(&n); err != nil || n == 0 {
		return 1 << 30
	}
	return n
}

// parity checks that every replacement produces today's pools exactly.
func parity(t *testing.T, ctx context.Context, app *sql.DB, user string, queries []string) {
	t.Helper()
	for _, q := range queries {
		var base []candidate
		others := map[string][]candidate{}
		err := inTx(ctx, app, user, func(tx *sql.Tx) error {
			var err error
			if base, err = loop(ctx, tx, q); err != nil {
				return err
			}
			for name, f := range map[string]func(context.Context, *sql.Tx, string) ([]candidate, error){
				"lateral": lateral, "window": window, "windowThenJoin": windowThenJoin, "prefiltered": prefiltered,
				"cursorWalk": cursorWalk,
			} {
				if others[name], err = f(ctx, tx, q); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("parity %s %q: %v", user, q, err)
		}
		if len(base) == 0 {
			t.Fatalf("%s %q: the loop found nothing, so parity would prove nothing", user, q)
		}
		for name, got := range others {
			if diff := compare(base, got); diff != "" {
				t.Errorf("%s %q: %s differs from the loop: %s", user, q, name, diff)
			}
		}
	}
	t.Logf("parity %s: every statement returned the loop's pools for %d queries", user, len(queries))
}

// compare reports how two pool sets differ, ignoring order among equal
// scores, which neither statement defines.
func compare(want, got []candidate) string {
	key := func(c candidate) string { return fmt.Sprintf("%s/%.9f", c.doc, c.score) }
	count := func(cs []candidate) map[string]int {
		m := map[string]int{}
		for _, c := range cs {
			m[key(c)]++
		}
		return m
	}
	w, g := count(want), count(got)
	var missing, extra int
	for k, n := range w {
		if g[k] < n {
			missing += n - g[k]
		}
	}
	for k, n := range g {
		if w[k] < n {
			extra += n - w[k]
		}
	}
	if missing == 0 && extra == 0 {
		return ""
	}
	return fmt.Sprintf("%d rows in total vs %d, %d missing, %d extra", len(want), len(got), missing, extra)
}

// leafStats compares the parent index's scores with the user's own leaf
// index, which pg_textsearch issue 552 says can differ.
func leafStats(t *testing.T, ctx context.Context, app *sql.DB, user, query string) {
	t.Helper()
	var leaf string
	err := app.QueryRowContext(ctx, `SELECT c.relname FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid JOIN pg_index x ON x.indexrelid = c.oid
		WHERE i.inhparent = 'chunks_bm25'::regclass AND x.indrelid = $1::regclass`, "chunks_"+user).Scan(&leaf)
	if err != nil {
		t.Fatalf("leaf index for %s: %v", user, err)
	}
	var parent, own float64
	var n int
	err = inTx(ctx, app, user, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*), coalesce(sum(p), 0), coalesce(sum(l), 0) FROM (
			SELECT `+scored+` AS p,
			       (chunks.heading_path || ' ' || chunks.heading_path || ' ' || chunks.text) <@> to_bm25query($1, '%s') AS l
			  FROM chunks ORDER BY `+scored+` LIMIT 50) s`, leaf), query).Scan(&n, &parent, &own)
	})
	if err != nil {
		t.Fatalf("leaf stats for %s: %v", user, err)
	}
	t.Logf("stats %s: top %d rows sum to %.4f through chunks_bm25 and %.4f through %s", user, n, parent, own, leaf)
}

// explain prints the plans of one document's candidate query and of the walk.
func explain(t *testing.T, ctx context.Context, app *sql.DB, user, query string, docs int) {
	t.Helper()
	doc := fmt.Sprintf("doc-%d", docs)
	plans := []struct {
		name, statement string
		args            []any
	}{
		{"loop, one document", `SELECT * FROM (
			SELECT chunks.doc_id, chunks.id, ` + scored + ` AS score, chunks.text
			  FROM chunks WHERE chunks.doc_id = $2
			 ORDER BY ` + scored + ` LIMIT 32) candidates WHERE score < 0 ORDER BY score`, []any{query, doc}},
		{"prefiltered, one document", `SELECT * FROM (
			SELECT chunks.doc_id, chunks.id, ` + scored + ` AS score, chunks.text
			  FROM chunks WHERE chunks.doc_id = $2 OFFSET 0) s
			 WHERE score < 0 ORDER BY score LIMIT 32`, []any{query, doc}},
		{"one walk + window", `SELECT ranked.doc_id, ranked.id, ranked.score, chunks.text FROM (
		  SELECT walk.*, row_number() OVER (PARTITION BY doc_id ORDER BY score, id) AS rn
		    FROM (SELECT chunks.library_id, chunks.doc_id, chunks.id, ` + scored + ` AS score
		            FROM chunks ORDER BY ` + scored + ` LIMIT 1000000) walk
		   WHERE score < 0) ranked
		  JOIN chunks ON chunks.library_id = ranked.library_id AND chunks.id = ranked.id
		 WHERE rn <= 32`, []any{query}},
	}
	for _, p := range plans {
		var lines []string
		err := inTx(ctx, app, user, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, `EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) `+p.statement, p.args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					return err
				}
				lines = append(lines, line)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("explain %s: %v", p.name, err)
		}
		t.Logf("== plan: %s, %s ==\n%s", p.name, user, strings.Join(lines, "\n"))
	}
}

// load writes docs ready documents into one user's library, each with 20 to
// 120 chunks of skewed pseudo-words, so a few terms are common and most are
// rare. It is the phase 0 harness's corpus.
func load(t *testing.T, owner *sql.DB, user string, from, docs int) {
	t.Helper()
	exec(t, owner, `
		INSERT INTO documents (library_id, doc_id, title, format, source_path, sha256, status, chunk_count)
		SELECT $1, 'doc-' || d, 'Document ' || d, 'pdf', '/spike/' || $1 || '/' || d, md5($1 || d), 'ready', 0
		  FROM generate_series($3::int + 1, $3::int + $2::int) AS d`, user, docs, from)
	exec(t, owner, `
		INSERT INTO chunks (library_id, doc_id, ordinal, section, heading_path, text)
		SELECT $1, 'doc-' || d, c, 's' || (c / 10),
		       'w' || (1 + floor(power(random(), 3) * $3::int)) || ' > w' || (1 + floor(power(random(), 3) * $3::int)),
		       (SELECT string_agg('w' || (1 + floor(power(random(), 3) * $3::int)::int), ' ')
		          FROM generate_series(1, $4::int) WHERE c > 0)
		  FROM generate_series($5::int + 1, $5::int + $2::int) AS d,
		       LATERAL generate_series(1, 20 + floor(random() * 100)::int) AS c`,
		user, docs, vocabulary, wordsPerChunk, from)
	exec(t, owner, `UPDATE documents SET chunk_count = n FROM (
		SELECT doc_id, count(*) AS n FROM chunks WHERE library_id = $1 GROUP BY doc_id) c
		WHERE documents.library_id = $1 AND documents.doc_id = c.doc_id`, user)
}

func pickQueries() []string {
	out := make([]string, 0, queriesPerRun)
	for i := range queriesPerRun {
		out = append(out, fmt.Sprintf("w%d w%d w%d", 40+i*7, 300+i*31, 1500+i*97))
	}
	return out
}

func measure(t *testing.T, queries []string, search func(string) error) string {
	t.Helper()
	var took []time.Duration
	for _, q := range queries {
		for i := range warmups + timedRuns {
			start := time.Now()
			if err := search(q); err != nil {
				t.Fatalf("search %q: %v", q, err)
			}
			if i >= warmups {
				took = append(took, time.Since(start))
			}
		}
	}
	slices.Sort(took)
	return fmt.Sprintf("p50 %7.1f p95 %7.1f ms", ms(took[len(took)/2]), ms(took[len(took)*95/100]))
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func inTx(ctx context.Context, db *sql.DB, user string, work func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"SELECT set_config('app.user_id', $1, true), set_config('app.library_id', $1, true)", user); err != nil {
		return err
	}
	return work(tx)
}

func collect(ctx context.Context, tx *sql.Tx, statement string, args ...any) ([]candidate, error) {
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		var text string
		if err := rows.Scan(&c.doc, &c.id, &c.score, &text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.doc, b.doc), cmp.Compare(a.score, b.score))
	})
	return out, rows.Err()
}

func strings1(ctx context.Context, tx *sql.Tx, statement string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, statement)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func exec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("%s: %v", strings.Fields(statement)[0:min(4, len(strings.Fields(statement)))], err)
	}
}
