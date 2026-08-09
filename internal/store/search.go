package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SearchResult is one hit returned by the search tool.
//
// Rank and Relevance are what callers see. The raw BM25 value is deliberately
// not exposed: SQLite's bm25() is negative and more-negative means better, so
// a reader comparing -10.2 against -8.2 concludes the wrong thing. It is kept
// unexported for ordering and logging only.
type SearchResult struct {
	DocID       string  `json:"doc_id"`
	Title       string  `json:"title"`
	HeadingPath string  `json:"heading_path"`
	Section     string  `json:"section,omitempty"`
	ChunkID     int64   `json:"chunk_id"`
	Rank        int     `json:"rank"`
	Relevance   float64 `json:"relevance"`
	PageStart   *int    `json:"page_start,omitempty"`
	PageEnd     *int    `json:"page_end,omitempty"`
	PrintedPage *int    `json:"printed_page_start,omitempty"`
	ImageCount  int     `json:"image_count"`
	Kind        string  `json:"kind,omitempty"`
	IndexBoost  bool    `json:"matched_book_index,omitempty"`
	Text        string  `json:"text"`

	bm25 float64 // raw, negative, lower is better
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
// named by a matching back-of-book index entry. SQLite's bm25() returns
// negative values where lower is better, so subtracting improves rank.
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

// Search runs BM25 over chunks_fts, weighting heading_path above text.
//
// bm25() weights are positional over the FTS columns (text, heading_path).
// heading_path is weighted higher because a query term appearing in a section
// title is far stronger evidence than the same term in body prose.
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
	rows, err := s.db.QueryContext(ctx,
		`SELECT doc_id FROM documents WHERE status = 'ready' ORDER BY doc_id`)
	if err != nil {
		return nil, err
	}
	var docIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		docIDs = append(docIDs, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	perDoc := make([][]SearchResult, 0, len(docIDs))
	for _, id := range docIDs {
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

	// Round-robin by within-document rank; relevance only breaks ties at the
	// same rank, never decides across ranks.
	var out []SearchResult
	for depth := 0; len(out) < p.K; depth++ {
		var tier []SearchResult
		for _, res := range perDoc {
			if depth < len(res) {
				tier = append(tier, res[depth])
			}
		}
		if len(tier) == 0 {
			break
		}
		sort.SliceStable(tier, func(i, j int) bool { return tier[i].bm25 < tier[j].bm25 })
		for _, r := range tier {
			if len(out) >= p.K {
				break
			}
			out = append(out, r)
		}
	}
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

	var boostSections []string
	if p.DocID != "" {
		boostSections, err = s.matchingIndexSections(ctx, p.DocID, p.Query)
		if err != nil {
			return nil, err
		}
	}

	args := []any{match}
	sb := strings.Builder{}
	sb.WriteString(`
		SELECT chunks.doc_id, documents.title, chunks.heading_path, chunks.section,
		       chunks.id, chunks.page_start, chunks.page_end, chunks.printed_page_start,
		       chunks.image_count, chunks.kind,
		       bm25(chunks_fts, 1.0, 2.0) AS score, chunks.text
		  FROM chunks_fts
		  JOIN chunks ON chunks.id = chunks_fts.rowid
		  JOIN documents ON documents.doc_id = chunks.doc_id
		 WHERE chunks_fts MATCH ?
		   AND documents.status = 'ready'`)
	if p.DocID != "" {
		sb.WriteString(" AND chunks.doc_id = ?")
		args = append(args, p.DocID)
	}
	if p.SectionFilter != "" {
		sb.WriteString(" AND (chunks.heading_path = ? OR chunks.heading_path LIKE ? || ' > %')")
		args = append(args, p.SectionFilter, p.SectionFilter)
	}
	// Over-fetch so the index boost can reorder within a meaningful pool
	// rather than only permuting an already-truncated top k.
	sb.WriteString(" ORDER BY score LIMIT ?")
	args = append(args, p.K*4)

	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		var section sql.NullString
		if err := rows.Scan(&r.DocID, &r.Title, &r.HeadingPath, &section, &r.ChunkID,
			&r.PageStart, &r.PageEnd, &r.PrintedPage, &r.ImageCount, &r.Kind, &r.bm25,
			&r.Text); err != nil {
			return nil, err
		}
		if section.Valid {
			r.Section = section.String
		}
		if AnySectionCovers(boostSections, r.Section) {
			r.IndexBoost = true
			r.bm25 -= indexBoost
		}
		if r.Kind == "keyword-reference" && !p.IncludeKeywordReference {
			r.bm25 += keywordReferencePenalty
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Re-sort: the boost changed the ordering the SQL produced.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].bm25 < out[j-1].bm25; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if len(out) > p.K {
		out = out[:p.K]
	}
	for i := range out {
		out[i].Rank = i + 1
		out[i].Relevance = relevance(out[i].bm25)
	}
	return out, nil
}

// matchingIndexSections finds back-of-book index entries matching the query
// and returns the sections they point at.
//
// Section references are resolved through SectionCovers by the caller, not by
// an ad hoc string comparison, so this shares one rule with the Python side.
func (s *Store) matchingIndexSections(ctx context.Context, docID, query string) ([]string, error) {
	words := wordRe.FindAllString(strings.ToLower(query), -1)
	if len(words) == 0 {
		return nil, nil
	}
	conds := make([]string, 0, len(words))
	args := []any{docID}
	for _, w := range words {
		if len(w) < 3 {
			continue
		}
		conds = append(conds, "instr(lower(term), ?) > 0")
		args = append(args, w)
	}
	if len(conds) == 0 {
		return nil, nil
	}
	q := `SELECT DISTINCT section FROM index_terms WHERE doc_id = ? AND (` +
		strings.Join(conds, " OR ") + `)`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var sec string
		if err := rows.Scan(&sec); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
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
		return "", fmt.Errorf("query contains no searchable terms")
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = `"` + w + `"`
	}
	return strings.Join(quoted, " OR "), nil
}

// summarizeWarnings turns a persisted StructureReport into a quality flag and
// human-readable findings. The worker is headless, so these were captured as
// data at ingest time; this is where a caller finally sees them.
func summarizeWarnings(raw sql.NullString) (string, []string) {
	if !raw.Valid || raw.String == "" {
		return "unknown", nil
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
		return "unknown", nil
	}
	quality := payload.Quality
	if quality == "" {
		quality = "unknown"
	}
	var notes []string
	add := func(items []string, label string) {
		if len(items) == 0 {
			return
		}
		shown := items
		if len(shown) > 10 {
			shown = shown[:10]
		}
		note := fmt.Sprintf("%d %s: %s", len(items), label, strings.Join(shown, ", "))
		if len(items) > len(shown) {
			note += fmt.Sprintf(" (+%d more)", len(items)-len(shown))
		}
		notes = append(notes, note)
	}
	add(payload.InTOCNotInBody, "sections in the table of contents but not the body")
	add(payload.InBodyNotInTOC, "headings in the body but not the table of contents")
	add(payload.DetectedMoreThanOne, "sections detected more than once")
	add(payload.Scattered, "sections spanning non-adjacent chunks")
	// Already phrased for a caller, so they pass through whole rather than
	// being summarised into a count of opaque identifiers.
	notes = append(notes, payload.Notes...)
	return quality, notes
}
