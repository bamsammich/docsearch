// Command postgres-spike asks whether Postgres can search the docsearch corpus
// as well as SQLite FTS5 does, with one user's rows isolated from another's.
//
// It copies an existing FTS5 index into Postgres under a user ID, then runs
// the committed labelled query set and the self-label probe through each
// engine using the same scoring code, so the only thing that differs between
// runs is the ranking. The FTS5 baseline goes through store.Search itself.
//
//	postgres-spike load     -engine E -pg URL -user U [-only DOC -times N]
//	postgres-spike eval     -engine E -pg URL -user U
//	postgres-spike rls      -engine E -pg URL
//	postgres-spike roundrobin -engine E -pg URL -user U
//
// Engines: fts5 (baseline, SQLite), textsearch (pg_textsearch), paradedb
// (pg_search), native (built-in ts_rank).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bamsammich/docsearch/internal/store"
)

var (
	engineName = flag.String("engine", "fts5", "fts5 | textsearch | paradedb | native")
	pgURL      = flag.String("pg", "", "superuser connection string")
	sqlitePath = flag.String("sqlite", "var/spike2/fts5.db", "source FTS5 index, and the baseline")
	queries    = flag.String("queries", "tests/retrieval/queries.json", "labelled query set")
	user       = flag.String("user", "alice", "user the rows belong to, and the eval runs as")
	only       = flag.String("only", "", "load only this doc_id")
	times      = flag.Int("times", 1, "load the rows this many times (distinct ids)")
	partition  = flag.Bool("partition", false, "textsearch: list-partition chunks by user")
	verbose    = flag.Bool("v", false, "print every query's hit position")
)

const appRole = "docsearch_app"

// fieldExpr is the single searchable field for textsearchf.
const fieldExpr = `heading_path || ' ' || heading_path || ' ' || text`

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	ctx := context.Background()
	src, err := store.Open(*sqlitePath)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = src.Close() }()

	switch flag.Arg(0) {
	case "load":
		pool := connect(ctx, *pgURL)
		defer pool.Close()
		must(load(ctx, pool, src))
	case "eval":
		s := searcherFor(ctx)
		must(evaluate(ctx, s, src))
	case "rls":
		pool := connect(ctx, *pgURL)
		defer pool.Close()
		must(checkRLS(ctx, pool))
	case "roundrobin":
		must(roundRobin(ctx, src))
	default:
		log.Fatalf("unknown command %q", flag.Arg(0))
	}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func connect(ctx context.Context, url string) *pgxpool.Pool {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		log.Fatal(err)
	}
	return pool
}

// appURL is the superuser URL with the unprivileged role swapped in. Every
// search runs as that role, so row-level security applies to it.
func appURL(url string) string {
	return strings.Replace(url, "postgres://postgres@", "postgres://"+appRole+"@", 1)
}

// -- schema and load --------------------------------------------------------

func schema() string {
	chunks := `CREATE TABLE chunks (
		id bigint NOT NULL, user_id text NOT NULL, doc_id text NOT NULL, ordinal int NOT NULL,
		section text, page_start int, page_end int, printed_page_start int,
		image_count int NOT NULL DEFAULT 0, kind text NOT NULL, url text, fragment text,
		heading_path text NOT NULL, text text NOT NULL`
	switch {
	case *engineName == "native":
		chunks += `, tsv tsvector GENERATED ALWAYS AS (
			setweight(to_tsvector('simple', heading_path), 'A') ||
			setweight(to_tsvector('simple', text), 'D')) STORED`
	}
	if *partition {
		chunks += `, PRIMARY KEY (user_id, id)) PARTITION BY LIST (user_id);`
	} else {
		chunks += `, PRIMARY KEY (id));`
	}
	return `
DROP TABLE IF EXISTS chunks, documents, index_terms CASCADE;
CREATE TABLE documents (user_id text NOT NULL, doc_id text NOT NULL, title text NOT NULL,
	status text NOT NULL, chunk_count int, PRIMARY KEY (user_id, doc_id));
` + chunks + `
CREATE INDEX chunks_doc ON chunks (user_id, doc_id);
CREATE TABLE index_terms (user_id text NOT NULL, doc_id text NOT NULL, term text NOT NULL,
	section text NOT NULL);
DO $$ BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '` + appRole + `') THEN
		CREATE ROLE ` + appRole + ` LOGIN NOBYPASSRLS;
	END IF;
END $$;
GRANT USAGE ON SCHEMA public TO ` + appRole + `;
GRANT SELECT ON documents, chunks, index_terms TO ` + appRole + `;
ALTER TABLE documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE documents FORCE ROW LEVEL SECURITY;
ALTER TABLE chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE chunks FORCE ROW LEVEL SECURITY;
ALTER TABLE index_terms ENABLE ROW LEVEL SECURITY;
ALTER TABLE index_terms FORCE ROW LEVEL SECURITY;
CREATE POLICY own ON documents USING (user_id = current_setting('app.user_id', true));
CREATE POLICY own ON chunks USING (user_id = current_setting('app.user_id', true));
CREATE POLICY own ON index_terms USING (user_id = current_setting('app.user_id', true));
`
}

func indexes() []string {
	switch *engineName {
	case "textsearch":
		return []string{
			`CREATE EXTENSION IF NOT EXISTS pg_textsearch`,
			`CREATE INDEX chunks_text_bm25 ON chunks USING bm25(text) WITH (text_config='simple')`,
			`CREATE INDEX chunks_heading_bm25 ON chunks USING bm25(heading_path) WITH (text_config='simple')`,
		}
	case "textsearchf":
		// One field, heading written twice then body: FTS5's bm25() with
		// column weights (1, 2) sums weighted term counts across columns
		// before saturating, under one IDF and one length. Two separate
		// indexes added together count a term in both heading and body twice
		// at full strength.
		return []string{
			`CREATE EXTENSION IF NOT EXISTS pg_textsearch`,
			`CREATE INDEX chunks_bm25f ON chunks USING bm25 ((` + fieldExpr + `)) WITH (text_config='simple')`,
		}
	case "paradedb":
		return []string{
			`CREATE EXTENSION IF NOT EXISTS pg_search`,
			`CREATE INDEX chunks_bm25 ON chunks USING paradedb (id, text, heading_path, doc_id, user_id)
			 WITH (key_field='id')`,
		}
	case "paradedbf":
		// The same one-field shape as textsearchf, with ParadeDB's default
		// tokenizer. An indexed expression needs a tokenizer cast and an alias.
		return []string{
			`CREATE EXTENSION IF NOT EXISTS pg_search`,
			`CREATE INDEX chunks_bm25 ON chunks USING paradedb (id,
			   ((` + fieldExpr + `)::pdb.unicode_words('alias=body')), doc_id, user_id)
			 WITH (key_field='id')`,
		}
	case "native":
		return []string{`CREATE INDEX chunks_tsv ON chunks USING gin (tsv)`}
	}
	return nil
}

// load copies the source index into Postgres for one user. The first load
// (re)creates the schema; a load for a second user appends to it and rebuilds
// the search indexes, so both users' rows share one index unless -partition.
func load(ctx context.Context, pool *pgxpool.Pool, src *store.Store) error {
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'chunks')`).Scan(&exists); err != nil {
		return err
	}
	fresh := !exists || *user == "alice"
	if fresh {
		if _, err := pool.Exec(ctx, schema()); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	if *partition {
		stmt := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS chunks_%s PARTITION OF chunks FOR VALUES IN ('%s')`,
			*user, *user)
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}

	docs, err := src.ListDocuments(ctx)
	if err != nil {
		return err
	}
	var idBase int64
	if !fresh {
		if err := pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) + 1 FROM chunks`).Scan(&idBase); err != nil {
			return err
		}
	}
	var chunkRows, termRows [][]any
	var docRows [][]any
	nulStripped := 0
	for copyN := 0; copyN < *times; copyN++ {
		for _, d := range docs {
			if *only != "" && d.DocID != *only {
				continue
			}
			docID := d.DocID
			if copyN > 0 {
				docID = fmt.Sprintf("%s-copy%d", d.DocID, copyN)
			}
			docRows = append(docRows, []any{*user, docID, d.Title, "ready", d.ChunkCount})
			rows, err := sourceChunks(ctx, d.DocID)
			if err != nil {
				return err
			}
			for _, c := range rows {
				// Postgres text cannot hold NUL; FTS5 stores it without
				// complaint. The port has to strip it at ingest.
				if strings.ContainsRune(c.text, 0) || strings.ContainsRune(c.heading, 0) {
					nulStripped++
					c.text = strings.ReplaceAll(c.text, "\x00", "")
					c.heading = strings.ReplaceAll(c.heading, "\x00", "")
				}
				chunkRows = append(chunkRows, []any{idBase + c.id + int64(copyN)*10_000_000, *user, docID,
					c.ordinal, c.section, c.pageStart, c.pageEnd, c.printed, c.images, c.kind,
					c.url, c.fragment, c.heading, c.text})
			}
			terms, err := sourceTerms(ctx, d.DocID)
			if err != nil {
				return err
			}
			for _, t := range terms {
				termRows = append(termRows, []any{*user, docID, t[0], t[1]})
			}
		}
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"documents"},
		[]string{"user_id", "doc_id", "title", "status", "chunk_count"}, pgx.CopyFromRows(docRows)); err != nil {
		return fmt.Errorf("copy documents: %w", err)
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"chunks"},
		[]string{"id", "user_id", "doc_id", "ordinal", "section", "page_start", "page_end",
			"printed_page_start", "image_count", "kind", "url", "fragment", "heading_path", "text"},
		pgx.CopyFromRows(chunkRows)); err != nil {
		return fmt.Errorf("copy chunks: %w", err)
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"index_terms"},
		[]string{"user_id", "doc_id", "term", "section"}, pgx.CopyFromRows(termRows)); err != nil {
		return fmt.Errorf("copy index_terms: %w", err)
	}

	// Rebuild the search indexes over everything now loaded, the way a
	// second user's documents would sit in one shared index in production.
	start := time.Now()
	for _, name := range []string{"chunks_text_bm25", "chunks_heading_bm25", "chunks_bm25", "chunks_tsv", "chunks_bm25f"} {
		if _, err := pool.Exec(ctx, "DROP INDEX IF EXISTS "+name); err != nil {
			return err
		}
	}
	for _, stmt := range indexes() {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	if _, err := pool.Exec(ctx, `ANALYZE`); err != nil {
		return err
	}
	var total int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM chunks`).Scan(&total)
	fmt.Printf("loaded %d chunks for %s (%d rows in table, index build %.2fs, NUL stripped from %d)\n",
		len(chunkRows), *user, total, time.Since(start).Seconds(), nulStripped)
	return nil
}

type srcChunk struct {
	id                          int64
	ordinal, images             int
	section, url, fragment      *string
	pageStart, pageEnd, printed *int
	kind, heading, text         string
}

// sourceChunks reads one document's chunks straight from the SQLite index.
func sourceChunks(ctx context.Context, docID string) ([]srcChunk, error) {
	db, err := sqliteDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, ordinal, section, page_start, page_end,
		printed_page_start, image_count, kind, url, fragment, heading_path, text
		FROM chunks WHERE doc_id = ? ORDER BY ordinal`, docID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []srcChunk
	for rows.Next() {
		var c srcChunk
		if err := rows.Scan(&c.id, &c.ordinal, &c.section, &c.pageStart, &c.pageEnd, &c.printed,
			&c.images, &c.kind, &c.url, &c.fragment, &c.heading, &c.text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func sourceTerms(ctx context.Context, docID string) ([][2]string, error) {
	db, err := sqliteDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT term, section FROM index_terms WHERE doc_id = ?`, docID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out [][2]string
	for rows.Next() {
		var t [2]string
		if err := rows.Scan(&t[0], &t[1]); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// -- searching --------------------------------------------------------------

// hit is one result. Score follows FTS5's convention, lower is better, so the
// store's boost and penalty constants apply unchanged.
type hit struct {
	ChunkID     int64
	DocID       string
	HeadingPath string
	Section     string
	Kind        string
	Score       float64
}

type searcher interface {
	// search ranks one document's chunks, or all documents' by round robin
	// when docID is empty, with the store's boost and penalty applied.
	search(ctx context.Context, query, docID string, k int) ([]hit, error)
	docIDs(ctx context.Context) ([]string, error)
}

func searcherFor(ctx context.Context) searcher {
	if *engineName == "fts5" {
		st, err := store.Open(*sqlitePath)
		if err != nil {
			log.Fatal(err)
		}
		return fts5{st}
	}
	cfg, err := pgxpool.ParseConfig(appURL(*pgURL))
	if err != nil {
		log.Fatal(err)
	}
	// The user is set per connection here. The service would set it per
	// transaction with SET LOCAL; for a single-user eval the effect is the same.
	cfg.ConnConfig.RuntimeParams["app.user_id"] = *user
	if strings.HasPrefix(*engineName, "paradedb") {
		// pgx caches prepared statements, and Postgres switches a statement
		// to a generic plan on its sixth execution. ParadeDB's ||| then
		// fails with "right-hand side must be a text value": the parameter
		// has no concrete value when the generic plan is built.
		cfg.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_custom_plan"
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	return pg{pool: pool, engine: *engineName}
}

type fts5 struct{ st *store.Store }

func (f fts5) search(ctx context.Context, query, docID string, k int) ([]hit, error) {
	res, err := f.st.Search(ctx, store.SearchParams{Query: query, DocID: docID, K: k})
	if err != nil {
		return nil, err
	}
	out := make([]hit, len(res))
	for i, r := range res {
		out[i] = hit{r.ChunkID, r.DocID, r.HeadingPath, r.Section, r.Kind, r.BM25()}
	}
	return out, nil
}

func (f fts5) docIDs(ctx context.Context) ([]string, error) {
	docs, err := f.st.ListDocuments(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.DocID
	}
	return out, nil
}

type pg struct {
	pool   *pgxpool.Pool
	engine string
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// Same constants as internal/store/search.go.
const (
	indexBoost              = 2.0
	keywordReferencePenalty = 6.0
)

// baseSQL returns matching chunks with a base score, lower is better, and the
// query argument it binds as $1. Only rows that match a query term come back,
// as with FTS5's MATCH. A scoped query binds the doc_id as $2; a limited one
// binds the row limit as the next parameter. Columns are id, doc_id,
// heading_path, section, kind, score.
func (p pg) baseSQL(query string, scoped, limited bool) (string, []any) {
	words := wordRe.FindAllString(query, -1)
	doc, limit, next := "true", "", 2
	if scoped {
		doc, next = "doc_id = $2", 3
	}
	if limited {
		limit = fmt.Sprintf("LIMIT $%d", next)
	}
	var sql string
	var arg any
	switch p.engine {
	case "textsearch":
		// Headings weigh twice body text, as bm25(chunks_fts, 1.0, 2.0) does.
		sql = `SELECT id, doc_id, heading_path, coalesce(section, '') AS section, kind, score FROM (
			SELECT *, 1.0 * (text <@> to_bm25query($1, 'chunks_text_bm25'))
			        + 2.0 * (heading_path <@> to_bm25query($1, 'chunks_heading_bm25')) AS score
			  FROM chunks WHERE {{DOC}}) s
			 WHERE score < 0 ORDER BY score {{LIMIT}}`
		arg = strings.Join(words, " ")
	case "textsearchf":
		// Ordered through the index; the score in the select list is
		// computed standalone, but only for the rows the limit keeps.
		sql = `SELECT id, doc_id, heading_path, coalesce(section, '') AS section, kind,
			       (` + fieldExpr + `) <@> to_bm25query($1, 'chunks_bm25f') AS score
			  FROM chunks WHERE {{DOC}}
			 ORDER BY (` + fieldExpr + `) <@> to_bm25query($1, 'chunks_bm25f') {{LIMIT}}`
		arg = strings.Join(words, " ")
	case "paradedb":
		sql = `SELECT id, doc_id, heading_path, coalesce(section, '') AS section, kind,
			       -pdb.score(id) AS score
			  FROM chunks
			 WHERE (text ||| $1::text OR heading_path ||| ($1::text)::pdb.boost(2)) AND {{DOC}}
			 ORDER BY pdb.score(id) DESC {{LIMIT}}`
		arg = strings.Join(words, " ")
	case "paradedbf":
		sql = `SELECT id, doc_id, heading_path, coalesce(section, '') AS section, kind,
			       -pdb.score(id) AS score
			  FROM chunks
			 WHERE (` + fieldExpr + `) ||| $1::text AND {{DOC}}
			 ORDER BY pdb.score(id) DESC {{LIMIT}}`
		arg = strings.Join(words, " ")
	case "native":
		// ts_rank is not BM25. Weight A (headings) is twice weight D (body);
		// the x10 puts scores in a range where the store's boost constants
		// still mean something. It is the floor, not a candidate.
		quoted := make([]string, len(words))
		for i, w := range words {
			quoted[i] = "'" + strings.ToLower(w) + "'"
		}
		sql = `SELECT id, doc_id, heading_path, coalesce(section, '') AS section, kind,
			       -10 * ts_rank('{0.5, 0.5, 0.5, 1.0}', tsv, q, 1) AS score
			  FROM chunks, to_tsquery('simple', $1) q
			 WHERE tsv @@ q AND {{DOC}}
			 ORDER BY score {{LIMIT}}`
		arg = strings.Join(quoted, " | ")
	default:
		panic("unknown engine " + p.engine)
	}
	sql = strings.NewReplacer("{{DOC}}", doc, "{{LIMIT}}", limit).Replace(sql)
	return sql, []any{arg}
}

func (p pg) searchDoc(ctx context.Context, query, docID string, k int) ([]hit, error) {
	sql, args := p.baseSQL(query, true, true)
	args = append(args, docID, k*4)
	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	var out []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.ChunkID, &h.DocID, &h.HeadingPath, &h.Section, &h.Kind, &h.Score); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	boost, err := p.boostSections(ctx, docID, query)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if store.AnySectionCovers(boost, out[i].Section) {
			out[i].Score -= indexBoost
		}
		if out[i].Kind == "keyword-reference" {
			out[i].Score += keywordReferencePenalty
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score < out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// boostSections mirrors store.matchingIndexSections.
func (p pg) boostSections(ctx context.Context, docID, query string) ([]string, error) {
	var conds []string
	args := []any{docID}
	for _, w := range wordRe.FindAllString(strings.ToLower(query), -1) {
		if len(w) < 3 {
			continue
		}
		args = append(args, w)
		conds = append(conds, fmt.Sprintf("position($%d in lower(term)) > 0", len(args)))
	}
	if len(conds) == 0 {
		return nil, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT section FROM index_terms WHERE doc_id = $1 AND (`+
		strings.Join(conds, " OR ")+`)`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (p pg) search(ctx context.Context, query, docID string, k int) ([]hit, error) {
	if docID != "" {
		return p.searchDoc(ctx, query, docID, k)
	}
	ids, err := p.docIDs(ctx)
	if err != nil {
		return nil, err
	}
	var perDoc [][]hit
	for _, id := range ids {
		res, err := p.searchDoc(ctx, query, id, k)
		if err != nil {
			return nil, err
		}
		if len(res) > 0 {
			perDoc = append(perDoc, res)
		}
	}
	return merge(perDoc, k), nil
}

// merge is store.searchAcrossDocuments' round robin.
func merge(perDoc [][]hit, k int) []hit {
	var out []hit
	for depth := 0; len(out) < k; depth++ {
		var tier []hit
		for _, res := range perDoc {
			if depth < len(res) {
				tier = append(tier, res[depth])
			}
		}
		if len(tier) == 0 {
			break
		}
		sort.SliceStable(tier, func(i, j int) bool { return tier[i].Score < tier[j].Score })
		for _, h := range tier {
			if len(out) >= k {
				break
			}
			out = append(out, h)
		}
	}
	return out
}

func (p pg) docIDs(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT doc_id FROM documents WHERE status = 'ready' ORDER BY doc_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// -- evaluation --------------------------------------------------------------

type queryFile struct {
	Documents map[string]struct {
		DocID string `json:"doc_id"`
		Match string `json:"match"`
	} `json:"documents"`
	Queries []struct {
		ID       string  `json:"id"`
		Doc      string  `json:"doc"`
		Expect   *string `json:"expect"`
		Category string  `json:"category"`
		Query    string  `json:"query"`
	} `json:"queries"`
}

// evaluate runs the labelled set and the self-label probe, both scoped the
// way docsearch-eval scopes them, and prints recall at 1/3/8/20.
func evaluate(ctx context.Context, s searcher, src *store.Store) error {
	raw, err := os.ReadFile(*queries)
	if err != nil {
		return err
	}
	var f queryFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	depths := []int{1, 3, 8, 20}
	var n int
	hits := map[int]int{}
	var latencies []time.Duration
	for _, q := range f.Queries {
		if q.Category == "cross-doc" || q.Expect == nil {
			continue
		}
		doc := f.Documents[q.Doc]
		start := time.Now()
		res, err := s.search(ctx, q.Query, doc.DocID, 20)
		latencies = append(latencies, time.Since(start))
		if err != nil {
			return fmt.Errorf("%s: %w", q.ID, err)
		}
		pos := -1
		for i, r := range res {
			ok := false
			if doc.Match == "section" {
				ok = store.SectionCovers(*q.Expect, r.Section)
			} else {
				ok = strings.Contains(strings.ToLower(r.HeadingPath), strings.ToLower(*q.Expect))
			}
			if ok {
				pos = i + 1
				break
			}
		}
		n++
		for _, d := range depths {
			if pos >= 1 && pos <= d {
				hits[d]++
			}
		}
		if *verbose {
			fmt.Printf("  %s hit@%d  %s\n", q.ID, pos, q.Query)
		}
	}
	fmt.Printf("%-10s labelled  n=%d", *engineName, n)
	for _, d := range depths {
		fmt.Printf("  @%d %3.0f%%", d, 100*float64(hits[d])/float64(n))
	}
	fmt.Printf("   (%d/%d/%d/%d)  median %s\n", hits[1], hits[3], hits[8], hits[20], median(latencies))

	// Self-label: the chunk's own heading and longest words, as selflabel.go.
	docs, err := src.ListDocuments(ctx)
	if err != nil {
		return err
	}
	var total int
	shits := map[int]int{}
	for _, d := range docs {
		stride := 1
		if d.ChunkCount != nil && *d.ChunkCount > 60 {
			stride = *d.ChunkCount / 60
		}
		sample, err := src.SampleChunks(ctx, d.DocID, stride)
		if err != nil {
			return err
		}
		for _, c := range sample {
			q := generateQuery(c)
			if len(wordsOf(q)) < 2 {
				continue
			}
			total++
			res, err := s.search(ctx, q, d.DocID, 20)
			if err != nil {
				return err
			}
			pos := -1
			for i, r := range res {
				if r.ChunkID == c.ChunkID {
					pos = i + 1
					for _, k := range depths {
						if i+1 <= k {
							shits[k]++
						}
					}
					break
				}
			}
			if *verbose && pos != 1 {
				fmt.Printf("  self %d@%d %s | %s\n", c.ChunkID, pos, d.DocID, q)
			}
		}
	}
	fmt.Printf("%-10s selflabel n=%d", *engineName, total)
	for _, d := range depths {
		fmt.Printf("  @%d %3.0f%%", d, 100*float64(shits[d])/float64(total))
	}
	fmt.Println()
	return nil
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2].Round(100 * time.Microsecond)
}

// generateQuery and wordsOf are copied from cmd/docsearch-eval so the
// self-label probe asks identical questions of every engine.
func generateQuery(c store.SampledChunk) string {
	leaf := c.HeadingPath
	if i := strings.LastIndex(leaf, " > "); i >= 0 {
		leaf = leaf[i+3:]
	}
	seen := map[string]bool{}
	for _, w := range wordsOf(leaf) {
		seen[w] = true
	}
	var terms []string
	for _, w := range wordsOf(c.Text) {
		if len(w) >= 5 && !seen[w] {
			seen[w] = true
			terms = append(terms, w)
		}
	}
	sort.SliceStable(terms, func(i, j int) bool { return len(terms[i]) > len(terms[j]) })
	if len(terms) > 4 {
		terms = terms[:4]
	}
	return strings.TrimSpace(leaf + " " + strings.Join(terms, " "))
}

func wordsOf(q string) []string {
	stop := map[string]bool{
		"the": true, "and": true, "for": true, "with": true, "how": true,
		"what": true, "why": true, "can": true, "not": true, "you": true,
		"are": true, "his": true, "her": true, "its": true, "from": true,
		"that": true, "this": true, "into": true, "out": true, "all": true,
		"does": true, "did": true, "was": true, "were": true, "have": true,
	}
	var out []string
	for _, w := range wordRe.FindAllString(strings.ToLower(q), -1) {
		if len(w) >= 3 && !stop[w] {
			out = append(out, w)
		}
	}
	return out
}
