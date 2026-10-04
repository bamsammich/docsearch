package pgstore

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/store/pgdbgen"
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
	if p.DocID == "" {
		return s.searchAcrossDocuments(ctx, p)
	}
	return s.searchOneDocument(ctx, p)
}

// searchAcrossDocuments merges per-document results by within-document rank.
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
// Cost is one query per ready document. That is fine at this scale and would
// need revisiting for a library of hundreds.
func (s *Store) searchAcrossDocuments(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	ready, err := s.readyTitles(ctx)
	if err != nil {
		return nil, err
	}

	perDoc := make([][]SearchResult, 0, len(ready))
	for _, id := range slices.Sorted(maps.Keys(ready)) {
		scoped := p
		scoped.DocID = id
		res, err := s.searchOneDocument(ctx, scoped)
		if err != nil {
			return nil, err
		}
		if len(res) > 0 {
			perDoc = append(perDoc, res)
		}
	}

	out := interleave(perDoc, p.K)
	for i := range out {
		out[i].Rank = i + 1
	}
	return out, nil
}

func (s *Store) searchOneDocument(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	match, err := ftsQuery(p.Query)
	if err != nil {
		return nil, err
	}

	// The search query reads chunks alone, because joining documents makes
	// the planner scan rather than walk the BM25 index. So the title and the
	// ready check, which the join used to supply, are asked for here: one
	// row per document rather than one per result.
	title, ready, err := s.documentLabel(ctx, p.DocID)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil
	}

	boostSections, err := s.matchingIndexSections(ctx, p.DocID, p.Query)
	if err != nil {
		return nil, err
	}

	statement, args := candidateQuery(p, match)
	var out []SearchResult
	err = pgsession.Query(ctx, s.db, s.userID, statement, args, func(rows *sql.Rows) error {
		var scanErr error
		out, scanErr = scanResults(rows, resultLabel{
			title:         title,
			boostSections: boostSections,
			keepKeywords:  p.IncludeKeywordReference,
		})
		return scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	return rankCandidates(out, p.K), nil
}

// candidateQuery builds the pool the ranking draws from.
//
// It over-fetches, so the index boost can reorder within a meaningful pool
// rather than only permuting an already-truncated top k.
func candidateQuery(p SearchParams, match string) (string, []any) {
	// The scored expression, written once. Ordering by anything else, or
	// filtering on the score inside the query, makes the planner score every
	// row standalone at about 150 microseconds each rather than walking the
	// index: the spike measured 159 ms that way against 13.4 ms this way.
	const scored = `(chunks.heading_path || ' ' || chunks.heading_path || ' ' ||
		chunks.text) <@> to_bm25query($1, 'chunks_bm25')`

	args := []any{match, p.DocID}
	sb := strings.Builder{}
	sb.WriteString(`SELECT * FROM (
		SELECT chunks.doc_id, chunks.heading_path, chunks.section,
		       chunks.id, chunks.page_start, chunks.page_end, chunks.printed_page_start,
		       chunks.image_count, chunks.kind, chunks.url, chunks.fragment,
		       ` + scored + ` AS score, chunks.text
		  FROM chunks
		 WHERE chunks.doc_id = $2`)
	if p.SectionFilter != "" {
		fmt.Fprintf(&sb,
			" AND (chunks.heading_path = $%d OR chunks.heading_path LIKE $%d || ' > %%')",
			len(args)+1, len(args)+1)
		args = append(args, p.SectionFilter)
	}
	fmt.Fprintf(&sb, " ORDER BY "+scored+" LIMIT $%d) candidates", len(args)+1)
	args = append(args, p.K*4)
	// A score of zero is a row that matched nothing. Where the index drives
	// the query none come back, but Postgres scans a table small enough to
	// scan and then scores every row, which a fresh library is. Dropping
	// them here costs nothing: the limit above already bounded the rows.
	sb.WriteString(" WHERE score < 0 ORDER BY score")
	return sb.String(), args
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

// matchingIndexSections finds back-of-book index entries matching the query
// and returns the sections they point at.
//
// Section references are resolved through SectionCovers by the caller, not by
// an ad hoc string comparison, so this shares one rule with the Python side.
func (s *Store) matchingIndexSections(ctx context.Context, docID, query string) ([]string, error) {
	conds, args := termConditions(docID, query)
	if len(conds) == 0 {
		return nil, nil
	}
	q := `SELECT DISTINCT section FROM index_terms WHERE doc_id = $1 AND (` +
		strings.Join(conds, " OR ") + `)`

	var out []string
	err := pgsession.Query(ctx, s.db, s.userID, q, args, func(rows *sql.Rows) error {
		for rows.Next() {
			var sec string
			if err := rows.Scan(&sec); err != nil {
				return err
			}
			out = append(out, sec)
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
func termConditions(docID, query string) ([]string, []any) {
	words := wordRe.FindAllString(strings.ToLower(query), -1)
	args := []any{docID}
	conds := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) < 3 {
			continue
		}
		// docID is already $1, so a word lands one past what is there.
		conds = append(conds, fmt.Sprintf("position($%d IN lower(term)) > 0", len(args)+1))
		args = append(args, w)
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

// warning is one finding of a structure report, with the phrasing a caller
// sees for it.
type warning struct {
	label string
	items []string
}

// warningNote renders one finding, naming at most ten of its subjects.
func warningNote(w warning) string {
	if len(w.items) == 0 {
		return ""
	}
	shown := w.items
	if len(shown) > 10 {
		shown = shown[:10]
	}
	note := fmt.Sprintf("%d %s: %s", len(w.items), w.label, strings.Join(shown, ", "))
	if len(w.items) > len(shown) {
		note += fmt.Sprintf(" (+%d more)", len(w.items)-len(shown))
	}
	return note
}

// summarizeWarnings turns a persisted StructureReport into a quality flag and
// human-readable findings. The worker is headless, so these were captured as
// data at ingest time; this is where a caller finally sees them.
func summarizeWarnings(raw sql.NullString) (string, []string) {
	if !raw.Valid || raw.String == "" {
		return qualityUnknown, nil
	}
	var payload struct {
		Quality             string   `json:"quality"`
		InTOCNotInBody      []string `json:"in_toc_not_in_body"`
		InBodyNotInTOC      []string `json:"in_body_not_in_toc"`
		DetectedMoreThanOne []string `json:"detected_more_than_once"`
		Scattered           []string `json:"scattered_sections"`
		Notes               []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(raw.String), &payload); err != nil {
		return qualityUnknown, nil
	}
	quality := payload.Quality
	if quality == "" {
		quality = qualityUnknown
	}
	var notes []string
	for _, w := range []warning{
		{items: payload.InTOCNotInBody,
			label: "sections in the table of contents but not the body"},
		{items: payload.InBodyNotInTOC,
			label: "headings in the body but not the table of contents"},
		{items: payload.DetectedMoreThanOne, label: "sections detected more than once"},
		{items: payload.Scattered, label: "sections spanning non-adjacent chunks"},
	} {
		if note := warningNote(w); note != "" {
			notes = append(notes, note)
		}
	}
	// Already phrased for a caller, so they pass through whole rather than
	// being summarised into a count of opaque identifiers.
	notes = append(notes, payload.Notes...)
	return quality, notes
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
func (s *Store) readyTitles(ctx context.Context) (map[string]string, error) {
	rows, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) ([]pgdbgen.ListReadyDocumentsRow, error) {
			return q.ListReadyDocuments(ctx)
		})
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
func (s *Store) documentLabel(ctx context.Context, docID string) (string, bool, error) {
	row, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) (pgdbgen.DocumentByIDRow, error) {
			return q.DocumentByID(ctx, docID)
		})
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
