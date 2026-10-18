package pgstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

// indexedDocument is one seeded document: a title and how many chunks it
// holds, which is what makes the pair lopsided.
type indexedDocument struct {
	id     string
	title  string
	chunks int
}

// buildIndex creates a two-document index where one document is deliberately
// much smaller, reproducing the lopsided shape that makes cross-document BM25
// scores incomparable.
func buildIndex(t *testing.T) *Store {
	t.Helper()
	st, _ := buildIndexWith(t)
	return st
}

// buildIndexWith is buildIndex, with the owner's connection for a case that
// seeds more rows.
func buildIndexWith(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	pg := pgtest.Start(t)
	// The migrations, the same ones a deployment runs and sqlc types its
	// queries against. Fatal rather than skipped: skipping would leave the
	// whole store suite green without having exercised anything.
	if err := schema.Create(t.Context(), pg.Owner); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	for _, d := range []indexedDocument{
		{id: "big", title: "Big Manual", chunks: 24},
		{id: "small", title: "Small Guide", chunks: 6},
	} {
		seedDocument(t, pg.Owner, d)
	}
	// The store reads as the restricted role a request uses, not as the
	// owner that seeded the rows.
	return New(pg.App, builtinUser), pg.Owner
}

func seedDocument(t *testing.T, raw *sql.DB, d indexedDocument) {
	t.Helper()
	write(t, raw, `INSERT INTO documents
		   (user_id,doc_id,title,format,source_path,sha256,status,chunk_count)
		 VALUES ($1,$2,$3,'pdf','/x',$4,'ready',$5)`,
		builtinUser, d.id, d.title, d.id, d.chunks)
	for i := 0; i < d.chunks; i++ {
		kind, text := chunkBody(d, i)
		write(t, raw, `INSERT INTO chunks
			   (user_id,doc_id,ordinal,section,heading_path,text,kind,image_count)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,0)`,
			builtinUser, d.id, i, "1."+itoa(i),
			d.title+" > Section "+itoa(i), text, kind)
	}
}

// write inserts one row as the owner, in a transaction that named the user the
// row belongs to.
func write(t *testing.T, raw *sql.DB, statement string, args ...any) {
	t.Helper()
	pgtest.WriteAs(t, raw, builtinUser, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), statement, args...); err != nil {
			t.Fatal(err)
		}
	})
}

// chunkBody is one chunk's kind and text.
//
// Keyword-reference entries are modelled as they actually are: short and
// term-dense, so BM25 favours them on incidental token overlap. Without a
// penalty they would take the top slots.
func chunkBody(d indexedDocument, i int) (string, string) {
	if d.id == "big" && i >= 14 {
		return "keyword-reference", "universe dmx universe dmx " + itoa(i)
	}
	return "prose", "This section explains how to configure the universe and " +
		"assign a dmx address to a patched fixture in the show file, " +
		"with worked examples and surrounding narrative " + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// An unscoped search must give every document a shot at the top slots rather
// than letting the smaller corpus sweep them on incomparable IDF.
func TestUnscopedSearchDoesNotLetOneDocumentSweepTheTop(t *testing.T) {
	st := buildIndex(t)
	res, err := st.Search(context.Background(), SearchParams{Query: "universe dmx", K: 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) < 2 {
		t.Fatalf("got %d results, want at least 2", len(res))
	}
	seen := map[string]bool{}
	for _, r := range res[:2] {
		seen[r.DocID] = true
	}
	if len(seen) != 2 {
		t.Errorf("top 2 results all came from %v; both documents should be represented "+
			"when results are merged by within-document rank", seen)
	}
}

// Scoped behaviour must be untouched by the cross-document merge.
func TestScopedSearchReturnsOnlyThatDocumentAndIsRanked(t *testing.T) {
	st := buildIndex(t)
	res, err := st.Search(context.Background(),
		SearchParams{Query: "universe dmx", DocID: "big", K: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("no results")
	}
	for _, r := range res {
		if r.DocID != "big" {
			t.Errorf("scoped search returned a chunk from %q", r.DocID)
		}
	}
	checkRankedBestFirst(t, res)
}

// checkRankedBestFirst asserts the two properties a ranked result set has:
// the score never improves further down the list, and the ranks run
// consecutively from one.
func checkRankedBestFirst(t *testing.T, res []SearchResult) {
	t.Helper()
	for i := 1; i < len(res); i++ {
		if res[i].bm25 < res[i-1].bm25 {
			t.Errorf("scoped results are not ordered best-first at position %d", i)
		}
		if res[i].Rank != i+1 {
			t.Errorf("rank = %d at position %d, want %d", res[i].Rank, i, i+1)
		}
	}
}

func TestRelevanceIsPositiveHigherIsBetterAndOrderPreserving(t *testing.T) {
	st := buildIndex(t)
	res, err := st.Search(context.Background(),
		SearchParams{Query: "universe dmx", DocID: "big", K: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Relevance < 0 || r.Relevance > 1 {
			t.Errorf("relevance %v outside 0..1", r.Relevance)
		}
	}
	for i := 1; i < len(res); i++ {
		if res[i].Relevance > res[i-1].Relevance {
			t.Errorf("relevance rose at position %d; it must be order-preserving", i)
		}
	}
}

// Keyword-reference entries are deprioritised, never dropped: a keyword lookup
// is a legitimate query and they are its correct answers.
func TestKeywordReferenceIsDeprioritisedButReachable(t *testing.T) {
	st := buildIndex(t)
	ctx := context.Background()

	def, err := st.Search(ctx, SearchParams{Query: "universe dmx", DocID: "big", K: 25})
	if err != nil {
		t.Fatal(err)
	}
	firstKeywordRank := bestKeywordRank(def)
	if firstKeywordRank == 0 {
		t.Fatal("keyword-reference chunks were excluded entirely; they must remain reachable")
	}
	if firstKeywordRank <= 5 {
		t.Errorf("first keyword-reference result at rank %d; it should be pushed down",
			firstKeywordRank)
	}

	inc, err := st.Search(ctx, SearchParams{
		Query: "universe dmx", DocID: "big", K: 25, IncludeKeywordReference: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if incFirst := bestKeywordRank(inc); incFirst >= firstKeywordRank {
		t.Errorf("include_keyword_reference did not lift them: rank %d vs %d",
			incFirst, firstKeywordRank)
	}
}

// bestKeywordRank is where the best keyword-reference entry landed, or zero
// when none came back at all.
func bestKeywordRank(res []SearchResult) int {
	for _, r := range res {
		if r.Kind == "keyword-reference" {
			return r.Rank
		}
	}
	return 0
}

// A finding captured at ingest is worthless if it stops at the database. The
// worker is headless, so this passthrough is the only way a caller learns that
// a document's structure does not separate its own chunks.
func TestSummarizeWarningsSurfacesStructureNotes(t *testing.T) {
	raw := sql.NullString{Valid: true, String: `{
		"quality": "degraded",
		"notes": ["35 chunks share only 10 distinct heading paths (0.29 per chunk)"],
		"scattered_sections": ["4.2"]
	}`}
	quality, notes := summarizeWarnings(raw)
	if quality != "degraded" {
		t.Fatalf("quality = %q, want degraded", quality)
	}
	var joined string
	for _, n := range notes {
		joined += n + "\n"
	}
	if !strings.Contains(joined, "35 chunks share only 10 distinct heading paths") {
		t.Errorf("structure note did not reach the caller, got: %v", notes)
	}
	if !strings.Contains(joined, "sections spanning non-adjacent chunks") {
		t.Errorf("existing findings must survive alongside notes, got: %v", notes)
	}
}

func TestSummarizeWarningsOnUningestedDocument(t *testing.T) {
	quality, notes := summarizeWarnings(sql.NullString{})
	if quality != "unknown" || notes != nil {
		t.Errorf("got (%q, %v), want (unknown, nil)", quality, notes)
	}
}

// builtinUser owns every row these cases write, which is what phase 04
// serves until authentication arrives.
const builtinUser = "default"

// A section filter is matched as written. Under LIKE, the underscore in
// "10_ Rules" would be a wildcard and sweep in "100 Rules".
func TestASectionFilterHasNoWildcards(t *testing.T) {
	pg := pgtest.Start(t)
	if err := schema.Create(t.Context(), pg.Owner); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	write(t, pg.Owner, `INSERT INTO documents
		   (user_id,doc_id,title,format,source_path,sha256,status,chunk_count)
		 VALUES ($1,'wild','Wild','pdf','/x','wild','ready',3)`, builtinUser)
	for i, heading := range []string{
		"Wild > 10_ Rules", "Wild > 10_ Rules > Detail", "Wild > 100 Rules > Detail",
	} {
		write(t, pg.Owner, `INSERT INTO chunks
			   (user_id,doc_id,ordinal,section,heading_path,text,kind,image_count)
			 VALUES ($1,'wild',$2,$3,$4,'calibrate the fixture before use','prose',0)`,
			builtinUser, i, itoa(i+1), heading)
	}
	st := New(pg.App, builtinUser)

	res, err := st.Search(t.Context(), SearchParams{
		Query: "calibrate fixture", DocID: "wild", SectionFilter: "Wild > 10_ Rules", K: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if strings.Contains(r.HeadingPath, "100 Rules") {
			t.Errorf("the filter matched %q, a section it does not name", r.HeadingPath)
		}
	}
	if len(res) != 2 {
		t.Errorf("got %d results, want the section and its child", len(res))
	}
}

// A chunk whose section a matching back-of-book entry names is lifted, both
// in a search of its document and in a search of the whole library, which
// reads every document's entries in one query.
func TestAnIndexEntryLiftsTheSectionItNames(t *testing.T) {
	st, owner := buildIndexWith(t)
	write(t, owner, `INSERT INTO index_terms (user_id, doc_id, term, section)
		VALUES ($1, 'big', 'Universe, patching a', '1.9')`, builtinUser)

	scoped, err := st.Search(t.Context(), SearchParams{Query: "universe", DocID: "big", K: 5})
	if err != nil {
		t.Fatal(err)
	}
	if best := firstOf(scoped, "big"); best.Section != "1.9" {
		t.Errorf("scoped: results are %v, want section 1.9 first", sections(scoped))
	}

	across, err := st.Search(t.Context(), SearchParams{Query: "universe", K: 6})
	if err != nil {
		t.Fatal(err)
	}
	if best := firstOf(across, "big"); best.Section != "1.9" {
		t.Errorf("across: big's best is %q, want section 1.9", best.Section)
	}
}

// firstOf is the best-ranked result from one document.
func firstOf(results []SearchResult, docID string) SearchResult {
	for _, r := range results {
		if r.DocID == docID {
			return r
		}
	}
	return SearchResult{}
}

func sections(results []SearchResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Section)
	}
	return out
}
