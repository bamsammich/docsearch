package pgstore

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
)

// SearchResult is one hit returned by the search tool.
//
// Rank and Relevance are what callers see. The raw BM25 value is deliberately
// not exposed: pg_textsearch's distance operator is negative and
// more-negative means better, as SQLite's bm25() was, so a reader comparing
// -10.2 against -8.2 concludes the wrong thing. It is kept unexported for
// ordering and logging only.
type SearchResult struct {
	PageStart   *int   `json:"page_start,omitempty"`
	PageEnd     *int   `json:"page_end,omitempty"`
	PrintedPage *int   `json:"printed_page_start,omitempty"`
	DocID       string `json:"doc_id"`
	Title       string `json:"title"`
	HeadingPath string `json:"heading_path"`
	Section     string `json:"section,omitempty"`
	Kind        string `json:"kind,omitempty"`
	// URL and Fragment address the page this chunk was read from. Both are
	// empty for a document ingested from a local file.
	URL       string  `json:"url,omitempty"`
	Fragment  string  `json:"fragment,omitempty"`
	Text      string  `json:"text"`
	ChunkID   int64   `json:"chunk_id"`
	Relevance float64 `json:"relevance"`
	bm25      float64 // raw, negative, lower is better
	Rank      int     `json:"rank"`

	ImageCount int  `json:"image_count"`
	IndexBoost bool `json:"matched_book_index,omitempty"`
}

// BM25 exposes the raw score for test harnesses and diagnostics.
func (r SearchResult) BM25() float64 { return r.bm25 }

// relevanceScale controls how quickly relevance saturates toward 1.
const relevanceScale = 10.0

// relevance maps a raw BM25 score to a monotonically increasing 0..1 value
// where higher is better.
//
// This is a presentation transform, not a probability. It exists because the
// raw sign convention is a trap for any reader, human or model. It is strictly
// order-preserving, so it never changes ranking -- and like the raw score it
// is only meaningful *within* one result set.
func relevance(bm25 float64) float64 {
	strength := -bm25
	if strength < 0 {
		strength = 0
	}
	return strength / (strength + relevanceScale)
}

// SearchParams are the inputs to Search.
type SearchParams struct {
	Query                   string
	DocID                   string
	SectionFilter           string
	K                       int
	IncludeKeywordReference bool
}

// indexBoost is subtracted from a BM25 score when the chunk's section was
// named by a matching back-of-book index entry. The score is negative and
// lower is better, so subtracting improves rank.
const indexBoost = 2.0

// keywordReferencePenalty pushes a self-declared keyword-reference entry down
// the ranking. It is a penalty, not an exclusion: a keyword lookup is a
// legitimate query and these chunks are its correct answers, reachable with
// IncludeKeywordReference.
//
// Such a family is term-dense and low-prose, so its entries match on incidental
// token overlap -- "step by step" surfacing StepOut, StepIn and StepFade. In
// this corpus the family is 326 of 944 chunks, so left alone it crowds a third
// of the index into every result set.
const keywordReferencePenalty = 6.0

// maxK is a safety net on result-set size, not the tool contract. The search
// tool's documented maximum of 25 is enforced in the MCP layer where it is
// declared; the store allows a deeper pool so diagnostics and reranking
// experiments can fetch candidates without changing what callers can ask for.
const maxK = 200

// Search runs BM25 over chunks, weighting the heading above the body.
//
// One index over one expression, with the heading written twice before the
// body: docs/research/postgres-spike.md measured that two indexes added
// together count a term present in both at full strength twice, and lose six
// points at depth 8 for it. Migration 7 declares the index; this has to order
// by the same expression verbatim or the planner will not use it.
func (s *Store) Search(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	if p.K <= 0 {
		p.K = 8
	}
	if p.K > maxK {
		p.K = maxK
	}
	match, err := ftsQuery(p.Query)
	if err != nil {
		return nil, err
	}
	// Every statement a search makes runs in one transaction, so a search
	// across the library opens and closes one rather than three for each
	// document it holds.
	var out []SearchResult
	err = pgsession.ReadOnly(ctx, s.db, s.userID, func(tx *sql.Tx) error {
		run := searcher{tx: tx, q: s.q.WithTx(tx), user: s.userID}
		index, err := run.ownIndex(ctx)
		if err != nil || index == "" {
			return err
		}
		run.index = index
		var searchErr error
		if p.DocID == "" {
			out, searchErr = run.acrossDocuments(ctx, p, match)
		} else {
			out, searchErr = run.oneDocument(ctx, p, match)
		}
		return searchErr
	})
	return out, err
}

// searcher is one search's statements, all in the transaction that named the
// user.
type searcher struct {
	tx *sql.Tx
	q  *pgdbgen.Queries
	// user is the account the transaction named, and index is the BM25
	// index of that account's own partition, which every score is read
	// through.
	user  string
	index string
}

// ownIndex is the name of the BM25 index on the account's own partition, or
// nothing for an account with no partition, which holds no chunks.
//
// pg_textsearch 1.4.0 scores a query naming the parent index, chunks_bm25,
// with the first partition's statistics (upstream issue 552), which are the
// built-in library's. Searched that way, another account's chunks scored
// zero for a term the built-in library lacks and were dropped, and scored
// with the wrong library's statistics for the rest.
//
// The partition is named by create_account. Postgres names the index, so it
// is read from the catalog rather than assumed. to_bm25query takes the name
// as it is stored, unquoted, even where an account's hyphen would need quotes
// in SQL.
func (r searcher) ownIndex(ctx context.Context) (string, error) {
	var index string
	err := r.tx.QueryRowContext(ctx, `SELECT pg_class.relname
		  FROM pg_inherits
		  JOIN pg_index ON pg_index.indexrelid = pg_inherits.inhrelid
		  JOIN pg_class ON pg_class.oid = pg_inherits.inhrelid
		 WHERE pg_inherits.inhparent = 'chunks_bm25'::regclass
		   AND pg_index.indrelid = to_regclass(format('%I', 'chunks_' || $1))`,
		r.user).Scan(&index)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find the library's search index: %w", err)
	}
	return index, nil
}

// bound is a scored statement's arguments: the three that scored and
// ownPartition read, then rest from $4 on.
//
// Every scored statement runs unnamed, as walk needs for its speed, so
// Postgres plans each one with the account and the index name in hand.
func (r searcher) bound(match string, rest ...any) []any {
	return append([]any{pgx.QueryExecModeCacheDescribe, match, r.index, r.user}, rest...)
}

// acrossDocuments merges per-document results by within-document rank.
//
// BM25 scores are not comparable across documents: IDF is computed over the
// whole table, so a term rare globally but common inside a small document lifts
// that document's chunks above better answers in a large one. Measured on this
// index, a corpus holding 22% of the chunks took 50% of the unscoped top-6.
//
// Comparing rank instead of score removes the incomparable quantity entirely:
// each document's best answer competes with every other document's best answer,
// its second with their seconds, and so on. Telling callers to scope by doc_id
// is not a substitute -- the caller who most needs scoping is exactly the one
// who does not yet know which document holds the answer.
//
// Every document's pool comes from one walk of the library's BM25 ordering,
// which walk describes, and the rows it keeps are scored in one statement.
// Each pool is then ranked exactly as a search of that document alone ranks
// it.
func (r searcher) acrossDocuments(
	ctx context.Context,
	p SearchParams,
	match string,
) ([]SearchResult, error) {
	ready, err := r.readyTitles(ctx)
	if err != nil {
		return nil, err
	}
	boosts, err := r.indexSections(ctx, "", p.Query)
	if err != nil {
		return nil, err
	}
	kept, err := r.walk(ctx, p, match, ready)
	if err != nil {
		return nil, err
	}
	rows, err := r.score(ctx, match, kept)
	if err != nil {
		return nil, err
	}

	pools := rows.pools(kept, ready, boosts, p.IncludeKeywordReference)
	perDoc := make([][]SearchResult, 0, len(pools))
	for _, id := range slices.Sorted(maps.Keys(pools)) {
		perDoc = append(perDoc, rankCandidates(pools[id], p.K))
	}

	out := interleave(perDoc, p.K)
	for i := range out {
		out[i].Rank = i + 1
	}
	return out, nil
}

// walk reads the library's BM25 ordering once and keeps each ready
// document's first pool of rows, which are the rows candidateQuery returns for
// that document. The identifiers come back in walk order, which is score
// order.
//
// A query per document cost the number of documents times the size of the
// library. pg_textsearch cannot narrow its ordered scan to one document: it
// orders the whole partition and filters on doc_id afterwards, so each
// document's query walked past every other document's matches. 500 documents
// in one library took 1.3 s that way.
//
// The walk is cheap only in one shape, and each condition below was measured
// over the same 5,800 chunks, where the walk takes 3 ms:
//
//   - It carries keys only. pg_textsearch 1.4.0 scores a projected <@>
//     standalone, at about 60 microseconds a row.
//   - Sorts are off. Without a LIMIT, row-level security leads the planner to
//     read the partition by its library_id index, score every row standalone and
//     sort, which took 260 ms.
//   - It runs as an unnamed statement. Executed from the driver's cached
//     prepared statement, the same plan took 190 ms.
//
// It also names the account's own index, which ownIndex explains. Once a
// second account exists, a scan through the parent's name rechecks each row
// at about 30 microseconds, and a 200-document library's walk took 98 ms
// that way against 5 ms.
func (r searcher) walk(
	ctx context.Context,
	p SearchParams,
	match string,
	ready map[string]string,
) ([]int64, error) {
	pools := walkPools{wanted: make(map[string]int, len(ready))}
	for id := range ready {
		pools.wanted[id] = poolSize(p.K)
	}
	statement, rest := walkQuery(p)
	err := r.withoutSorts(ctx, func() error {
		return pgsession.Rows(ctx, r.tx, statement, r.bound(match, rest...), pools.read)
	})
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	return pools.kept, nil
}

// walkQuery is every chunk's identifier in BM25 order, within the section
// filter if there is one, and the arguments it takes after bound's.
func walkQuery(p SearchParams) (string, []any) {
	var rest []any
	statement := `SELECT chunks.doc_id, chunks.id FROM chunks WHERE ` + ownPartition
	if p.SectionFilter != "" {
		statement += " AND " + sectionWithin(len(rest))
		rest = append(rest, p.SectionFilter)
	}
	return statement + " ORDER BY " + scored, rest
}

// walkPools collects the identifiers a walk keeps.
type walkPools struct {
	// wanted is how many more rows each document's pool takes. A document
	// still being ingested is never in it.
	wanted map[string]int
	kept   []int64
}

func (w *walkPools) read(rows *sql.Rows) error {
	for rows.Next() {
		var doc string
		var id int64
		if err := rows.Scan(&doc, &id); err != nil {
			return err
		}
		if w.wanted[doc] > 0 {
			w.kept = append(w.kept, id)
			w.wanted[doc]--
		}
	}
	return nil
}

// withoutSorts runs one statement with sorts disabled, and only that one.
func (r searcher) withoutSorts(ctx context.Context, run func() error) error {
	if _, err := r.tx.ExecContext(ctx, "SET LOCAL enable_sort = off"); err != nil {
		return err
	}
	if err := run(); err != nil {
		return err
	}
	_, err := r.tx.ExecContext(ctx, "SET LOCAL enable_sort TO DEFAULT")
	return err
}

// score reads and scores the rows walk kept.
func (r searcher) score(ctx context.Context, match string, ids []int64) (scoredRows, error) {
	out := make(scoredRows, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	statement := `SELECT chunks.doc_id, chunks.heading_path, chunks.section,
	       chunks.id, chunks.page_start, chunks.page_end, chunks.printed_page_start,
	       chunks.image_count, chunks.kind, chunks.url, chunks.fragment,
	       ` + scored + ` AS score, chunks.text
	  FROM chunks
	 WHERE ` + ownPartition + ` AND chunks.id = ANY($4)`
	if err := pgsession.Rows(ctx, r.tx, statement, r.bound(match, ids), out.read); err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	return out, nil
}

// scoredRows are the rows score read, by identifier.
type scoredRows map[int64]SearchResult

// read keeps the rows that matched.
//
// A row scoring zero matched nothing. The index walk returns none, but a
// scan and a sort, which disabling sorts discourages and does not forbid,
// returns every row. Those sort after the matches, so they only ever fill a
// pool's tail, and dropping them leaves the matches. They are dropped here
// rather than in SQL, where the planner moves the test into the scan and
// scores every row twice.
func (s scoredRows) read(rows *sql.Rows) error {
	for rows.Next() {
		res, err := scanResult(rows)
		if err != nil {
			return err
		}
		if res.bm25 < 0 {
			s[res.ChunkID] = res
		}
	}
	return nil
}

// pools groups the rows by document with the adjustments applied. Each pool
// keeps walk order, which is score order, so it reaches rankCandidates in the
// order a document's own query returns it.
func (s scoredRows) pools(
	kept []int64,
	ready map[string]string,
	boosts map[string][]string,
	keepKeywords bool,
) map[string][]SearchResult {
	out := map[string][]SearchResult{}
	for _, id := range kept {
		res, ok := s[id]
		if !ok {
			continue
		}
		res.Title = ready[res.DocID]
		out[res.DocID] = append(out[res.DocID], adjusted(res, resultLabel{
			boostSections: boosts[res.DocID],
			keepKeywords:  keepKeywords,
		}))
	}
	return out
}

// oneDocument searches the document p names.
//
// The candidate query reads chunks alone, because joining documents makes the
// planner scan rather than walk the BM25 index. So the title and the ready
// check, which the join used to supply, are asked for once beforehand.
func (r searcher) oneDocument(
	ctx context.Context,
	p SearchParams,
	match string,
) ([]SearchResult, error) {
	title, ready, err := r.documentLabel(ctx, p.DocID)
	if err != nil || !ready {
		return nil, err
	}
	boosts, err := r.indexSections(ctx, p.DocID, p.Query)
	if err != nil {
		return nil, err
	}
	return r.candidates(ctx, p, match, resultLabel{
		title:         title,
		boostSections: boosts[p.DocID],
		keepKeywords:  p.IncludeKeywordReference,
	})
}

// candidates are one document's top k, ranked.
func (r searcher) candidates(
	ctx context.Context,
	p SearchParams,
	match string,
	label resultLabel,
) ([]SearchResult, error) {
	statement, rest := candidateQuery(p)
	args := r.bound(match, rest...)
	var out []SearchResult
	err := pgsession.Rows(ctx, r.tx, statement, args, func(rows *sql.Rows) error {
		var scanErr error
		out, scanErr = scanResults(rows, label)
		return scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	return rankCandidates(out, p.K), nil
}

// scored is the expression the BM25 index orders by, written once. Ordering
// by anything else, or filtering on the score inside the query, makes the
// planner score every row standalone at about 150 microseconds each rather
// than walking the index: the spike measured 159 ms that way against 13.4 ms
// this way.
//
// $1 is the query and $2 the account's own index, which ownIndex finds.
const scored = `(chunks.heading_path || ' ' || chunks.heading_path || ' ' ||
	chunks.text) <@> to_bm25query($1, $2)`

// ownPartition fixes the partition when the statement is planned. Row-level
// security already limits the rows to the account's, but through a setting
// the planner does not read, so without the test it plans for every
// partition and pg_textsearch refuses the index that scored names.
const ownPartition = `chunks.library_id = $3`

// sectionWithin tests a chunk against the section filter, which a statement
// passes after n arguments of its own.
func sectionWithin(n int) string {
	at := boundArgs + n + 1
	return fmt.Sprintf(
		"(chunks.heading_path = $%d OR starts_with(chunks.heading_path, $%d || ' > '))", at, at)
}

// boundArgs is how many arguments bound puts before a statement's own.
const boundArgs = 3

// poolSize is how many candidates a document's pool holds for a search of k.
//
// It over-fetches, so the index boost can reorder within a meaningful pool
// rather than only permuting an already-truncated top k.
func poolSize(k int) int { return k * 4 }

// candidateQuery builds the pool the ranking draws from, poolSize rows deep,
// and the arguments it takes after bound's.
func candidateQuery(p SearchParams) (string, []any) {
	rest := []any{p.DocID}
	sb := strings.Builder{}
	sb.WriteString(`SELECT * FROM (
		SELECT chunks.doc_id, chunks.heading_path, chunks.section,
		       chunks.id, chunks.page_start, chunks.page_end, chunks.printed_page_start,
		       chunks.image_count, chunks.kind, chunks.url, chunks.fragment,
		       ` + scored + ` AS score, chunks.text
		  FROM chunks
		 WHERE ` + ownPartition + ` AND chunks.doc_id = $4`)
	if p.SectionFilter != "" {
		sb.WriteString(" AND " + sectionWithin(len(rest)))
		rest = append(rest, p.SectionFilter)
	}
	fmt.Fprintf(&sb, " ORDER BY "+scored+" LIMIT $%d) candidates", boundArgs+len(rest)+1)
	rest = append(rest, poolSize(p.K))
	// A score of zero is a row that matched nothing. Where the index drives
	// the query none come back, but Postgres scans a table small enough to
	// scan and then scores every row, which a fresh library is. Dropping
	// them here costs nothing: the limit above already bounded the rows.
	sb.WriteString(" WHERE score < 0 ORDER BY score")
	return sb.String(), rest
}

// rankCandidates orders the candidates and numbers the top k.
//
// The sort is here rather than in SQL because the two adjustments scanResults
// applies changed the ordering the query produced.
func rankCandidates(out []SearchResult, k int) []SearchResult {
	slices.SortStableFunc(out, func(a, b SearchResult) int {
		return cmp.Compare(a.bm25, b.bm25)
	})
	if len(out) > k {
		out = out[:k]
	}
	for i := range out {
		out[i].Rank = i + 1
		out[i].Relevance = relevance(out[i].bm25)
	}
	return out
}

// indexSections finds back-of-book index entries matching the query and
// returns the sections they point at, by document: every document's at once,
// or the one docID names.
//
// Section references are resolved through SectionCovers by the caller, not by
// an ad hoc string comparison, so this shares one rule with the Python side.
func (r searcher) indexSections(
	ctx context.Context,
	docID, query string,
) (map[string][]string, error) {
	conds, args := termConditions(query)
	if len(conds) == 0 {
		return nil, nil
	}
	q := `SELECT DISTINCT doc_id, section FROM index_terms WHERE (` +
		strings.Join(conds, " OR ") + `)`
	if docID != "" {
		args = append(args, docID)
		q += fmt.Sprintf(" AND doc_id = $%d", len(args))
	}

	out := map[string][]string{}
	err := pgsession.Rows(ctx, r.tx, q, args, func(rows *sql.Rows) error {
		for rows.Next() {
			var doc, section string
			if err := rows.Scan(&doc, &section); err != nil {
				return err
			}
			out[doc] = append(out[doc], section)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// termConditions tests each query word against the index terms as a
// substring, which is what finds "caching" under an entry reading "cache,
// write-through". Words shorter than three characters are dropped: as a
// substring they match most of a back-of-book index.
func termConditions(query string) ([]string, []any) {
	words := wordRe.FindAllString(strings.ToLower(query), -1)
	args := make([]any, 0, len(words))
	conds := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) < 3 {
			continue
		}
		args = append(args, w)
		conds = append(conds, fmt.Sprintf("position($%d IN lower(term)) > 0", len(args)))
	}
	return conds, args
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// ftsQuery converts free text into an FTS5 MATCH expression.
//
// Every token is quoted. Unquoted user input reaches the FTS5 query parser,
// where characters like '"', '*', ':', '^', 'NEAR' and 'OR' are operators --
// a stray quote is a syntax error surfaced to the caller, and the rest change
// the query's meaning in ways the caller did not ask for.
func ftsQuery(raw string) (string, error) {
	words := wordRe.FindAllString(raw, -1)
	if len(words) == 0 {
		return "", errors.New("query contains no searchable terms")
	}
	// Space-separated terms, which to_bm25query reads as any-of, the same
	// thing FTS5's "a" OR "b" asked for. No quoting: the words are already
	// what the word pattern matched, so none of them carries syntax.
	return strings.Join(words, " "), nil
}

// interleave merges per-document results by within-document rank.
//
// Relevance only breaks ties at the same rank, never decides across ranks,
// which is what keeps an incomparable score out of the ordering: each
// document's best answer competes with every other document's best answer.
func interleave(perDoc [][]SearchResult, k int) []SearchResult {
	var out []SearchResult
	for depth := 0; len(out) < k; depth++ {
		tier := tierAt(perDoc, depth)
		if len(tier) == 0 {
			break
		}
		slices.SortStableFunc(tier, func(a, b SearchResult) int {
			return cmp.Compare(a.bm25, b.bm25)
		})
		out = append(out, tier[:min(len(tier), k-len(out))]...)
	}
	return out
}

// tierAt is every document's result at one within-document rank.
func tierAt(perDoc [][]SearchResult, depth int) []SearchResult {
	tier := make([]SearchResult, 0, len(perDoc))
	for _, res := range perDoc {
		if depth < len(res) {
			tier = append(tier, res[depth])
		}
	}
	return tier
}

// readyTitles is every searchable document, by identifier.
func (r searcher) readyTitles(ctx context.Context) (map[string]string, error) {
	rows, err := r.q.ListReadyDocuments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the searchable documents: %w", err)
	}
	ready := make(map[string]string, len(rows))
	for _, row := range rows {
		ready[row.DocID] = row.Title
	}
	return ready, nil
}

// documentLabel is a document's title, and whether it is searchable at all.
func (r searcher) documentLabel(ctx context.Context, docID string) (string, bool, error) {
	row, err := r.q.DocumentByID(ctx, docID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", docID, err)
	}
	return row.Title, row.Status == "ready", nil
}

// resultLabel is what a scan needs beyond the row: the facts the query no
// longer carries, and the two adjustments the ranking applies afterwards.
type resultLabel struct {
	title         string
	boostSections []string
	keepKeywords  bool
}

// scanResults reads the candidates and applies the two score adjustments.
func scanResults(rows *sql.Rows, label resultLabel) ([]SearchResult, error) {
	var out []SearchResult
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		r.Title = label.title
		out = append(out, adjusted(r, label))
	}
	return out, nil
}

func scanResult(rows *sql.Rows) (SearchResult, error) {
	var r SearchResult
	var section, url, fragment sql.NullString
	if err := rows.Scan(&r.DocID, &r.HeadingPath, &section, &r.ChunkID,
		&r.PageStart, &r.PageEnd, &r.PrintedPage, &r.ImageCount, &r.Kind,
		&url, &fragment, &r.bm25, &r.Text); err != nil {
		return r, err
	}
	r.Section, r.URL, r.Fragment = section.String, url.String, fragment.String
	return r, nil
}

// adjusted applies the two score adjustments the query cannot make, because
// each depends on something it does not know: which sections a matching
// back-of-book index entry named, and whether the caller asked for
// keyword-reference entries.
func adjusted(r SearchResult, label resultLabel) SearchResult {
	if AnySectionCovers(label.boostSections, r.Section) {
		r.IndexBoost = true
		r.bm25 -= indexBoost
	}
	if r.Kind == "keyword-reference" && !label.keepKeywords {
		r.bm25 += keywordReferencePenalty
	}
	return r
}
