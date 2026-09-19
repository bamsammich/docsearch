package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bamsammich/docsearch/internal/store"
)

var (
	sqliteOnce sync.Once
	sqliteConn *sql.DB
	sqliteErr  error
)

// sqliteDB opens the source index read-only. The driver is registered by the
// store package's import of modernc.org/sqlite.
func sqliteDB() (*sql.DB, error) {
	sqliteOnce.Do(func() {
		sqliteConn, sqliteErr = sql.Open("sqlite", "file:"+*sqlitePath+"?mode=ro")
	})
	return sqliteConn, sqliteErr
}

// -- row-level security -------------------------------------------------------

// checkRLS asks whether the policy holds when the application forgets to
// filter. Each check runs as the unprivileged role against a table holding
// at least two users' rows.
func checkRLS(ctx context.Context, admin *pgxpool.Pool) error {
	var users []string
	rows, err := admin.Query(ctx, `SELECT DISTINCT user_id FROM chunks ORDER BY 1`)
	if err != nil {
		return err
	}
	if users, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return err
	}
	if len(users) < 2 {
		return fmt.Errorf("load a second user first; found %v", users)
	}
	totals := map[string]int{}
	for _, u := range users {
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE user_id = $1`, u).Scan(&n); err != nil {
			return err
		}
		totals[u] = n
	}
	fmt.Printf("rows per user, as superuser: %v\n", totals)

	conn, err := pgx.Connect(ctx, appURL(*pgURL))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	report := func(name string, ok bool, detail string) {
		mark := "PASS"
		if !ok {
			mark = "FAIL"
		}
		fmt.Printf("%s  %-52s %s\n", mark, name, detail)
	}

	var n int
	err = conn.QueryRow(ctx, `SELECT count(*) FROM chunks`).Scan(&n)
	report("no user set: sees nothing", err == nil && n == 0, fmt.Sprintf("rows=%d err=%v", n, err))

	for _, u := range users {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.user_id', $1, true)`, u); err != nil {
			return err
		}
		// No WHERE on user_id: the forgotten filter.
		var own, foreign int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE user_id = $1),
			count(*) FILTER (WHERE user_id <> $1) FROM chunks`, u).Scan(&own, &foreign); err != nil {
			return err
		}
		report(fmt.Sprintf("user %s, unfiltered count", u), own == totals[u] && foreign == 0,
			fmt.Sprintf("own=%d foreign=%d", own, foreign))

		// A search through the engine's index, unfiltered by user, must
		// return a full page of this user's rows and none of anyone else's.
		p := pg{engine: *engineName}
		sqlText, args := p.baseSQL("fixture patch universe", false, true)
		args = append(args, 20)
		rows, err := tx.Query(ctx, sqlText, args...)
		if err != nil {
			return fmt.Errorf("search as %s: %w", u, err)
		}
		got, foreignHits := 0, 0
		for rows.Next() {
			var h hit
			if err := rows.Scan(&h.ChunkID, &h.DocID, &h.HeadingPath, &h.Section, &h.Kind, &h.Score); err != nil {
				rows.Close()
				return err
			}
			got++
			var owner string
			if err := admin.QueryRow(ctx, `SELECT user_id FROM chunks WHERE id = $1`, h.ChunkID).Scan(&owner); err != nil {
				rows.Close()
				return err
			}
			if owner != u {
				foreignHits++
			}
		}
		rows.Close()
		report(fmt.Sprintf("user %s, index search without user filter", u), got == 20 && foreignHits == 0,
			fmt.Sprintf("results=%d foreign=%d", got, foreignHits))

		var plan string
		prow, err := tx.Query(ctx, "EXPLAIN "+sqlText, args...)
		if err != nil {
			return err
		}
		lines, err := pgx.CollectRows(prow, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, l := range lines {
			if strings.Contains(l, "Index Scan") || strings.Contains(l, "Custom Scan") ||
				strings.Contains(l, "Bitmap") || strings.Contains(l, "Seq Scan") {
				plan = strings.TrimSpace(l)
				break
			}
		}
		fmt.Printf("      plan: %s\n", plan)
		if err := tx.Rollback(ctx); err != nil {
			return err
		}
	}

	var errWrite error
	_, errWrite = conn.Exec(ctx, `INSERT INTO chunks (id, user_id, doc_id, ordinal, kind, heading_path, text)
		VALUES (-1, 'mallory', 'x', 0, 'prose', 'x', 'x')`)
	report("app role cannot write", errWrite != nil, fmt.Sprintf("err=%v", errWrite))
	return nil
}

// -- round robin in one query ---------------------------------------------------

// roundRobin checks that unscoped search can be one SQL statement: per-document
// rank by row_number() over the base score with boost and penalty applied in
// SQL, then ordered by that rank. It compares the result with the per-document
// loop the store runs today, query by query, and times both.
func roundRobin(ctx context.Context, src *store.Store) error {
	s, ok := searcherFor(ctx).(pg)
	if !ok {
		return fmt.Errorf("roundrobin needs a Postgres engine")
	}
	f, err := readQueries()
	if err != nil {
		return err
	}

	var same, total int
	var loopTimes, oneTimes []time.Duration
	for _, q := range f {
		start := time.Now()
		loop, err := s.search(ctx, q, "", 8)
		loopTimes = append(loopTimes, time.Since(start))
		if err != nil {
			return err
		}
		start = time.Now()
		one, err := s.oneQuery(ctx, q, 8)
		oneTimes = append(oneTimes, time.Since(start))
		if err != nil {
			return err
		}
		total++
		if equalIDs(loop, one) {
			same++
		} else if *verbose {
			fmt.Printf("  differs: %q\n    loop %v\n    one  %v\n", q, ids(loop), ids(one))
		}
	}
	fmt.Printf("%-10s round robin: %d/%d queries identical; per-document loop median %s, one query median %s\n",
		*engineName, same, total, median(loopTimes), median(oneTimes))
	return nil
}

func readQueries() ([]string, error) {
	var f queryFile
	raw, err := os.ReadFile(*queries)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	var out []string
	for _, q := range f.Queries {
		out = append(out, q.Query)
	}
	return out, nil
}

// oneQuery is unscoped search as a single statement. A LATERAL join runs the
// scoped base query once per document with the store's 4k candidate cap, so
// every document's pool is the one the per-document loop would fetch, without
// a round trip each.
func (p pg) oneQuery(ctx context.Context, query string, k int) ([]hit, error) {
	scoped, args := p.baseSQL(query, true, true)
	scoped = strings.Replace(scoped, "doc_id = $2", "doc_id = d.doc_id", 1)
	scoped = strings.Replace(scoped, "LIMIT $3", "LIMIT $2", 1)
	args = append(args, k*4)
	base := `SELECT c.* FROM documents d CROSS JOIN LATERAL (` + scoped + `) c WHERE d.status = 'ready'`
	var conds []string
	for _, w := range wordRe.FindAllString(strings.ToLower(query), -1) {
		if len(w) < 3 {
			continue
		}
		args = append(args, w)
		conds = append(conds, fmt.Sprintf("position($%d in lower(term)) > 0", len(args)))
	}
	boost := "SELECT NULL::text AS doc_id, NULL::text AS section WHERE false"
	if len(conds) > 0 {
		boost = "SELECT DISTINCT doc_id, section FROM index_terms WHERE " + strings.Join(conds, " OR ")
	}
	args = append(args, k)
	sqlText := fmt.Sprintf(`
		WITH base AS (%s),
		     boost AS (%s),
		     scored AS (
		       SELECT b.*, b.score
		         - CASE WHEN EXISTS (SELECT 1 FROM boost x WHERE x.doc_id = b.doc_id
		                  AND (b.section = x.section OR b.section LIKE x.section || '.%%'))
		                THEN %v ELSE 0 END
		         + CASE WHEN b.kind = 'keyword-reference' THEN %v ELSE 0 END AS final
		         FROM base b),
		     ranked AS (
		       SELECT *, row_number() OVER (PARTITION BY doc_id ORDER BY final) AS rn FROM scored)
		SELECT id, doc_id, heading_path, section, kind, final FROM ranked
		 WHERE rn <= $%d ORDER BY rn, final LIMIT $%d`,
		base, boost, indexBoost, keywordReferencePenalty, len(args), len(args))
	rows, err := p.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.ChunkID, &h.DocID, &h.HeadingPath, &h.Section, &h.Kind, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func ids(hs []hit) []int64 {
	out := make([]int64, len(hs))
	for i, h := range hs {
		out[i] = h.ChunkID
	}
	return out
}

func equalIDs(a, b []hit) bool {
	x, y := ids(a), ids(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
