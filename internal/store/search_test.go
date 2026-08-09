package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildIndex creates a two-document index where one document is deliberately
// much smaller, reproducing the lopsided shape that makes cross-document BM25
// scores incomparable.
func buildIndex(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The schema itself, the same file docsearch.db creates databases from
	// and sqlc types its queries against. Read rather than copied, and fatal
	// rather than skipped: skipping would leave the whole store suite green
	// without having exercised anything.
	schema, err := os.ReadFile("../../python/docsearch/schema.sql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := raw.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct {
		id     string
		title  string
		chunks int
	}{{"big", "Big Manual", 24}, {"small", "Small Guide", 6}} {
		if _, err := raw.Exec(
			`INSERT INTO documents (doc_id,title,format,source_path,sha256,status,chunk_count)
			 VALUES (?,?,'pdf','/x',?, 'ready', ?)`, d.id, d.title, d.id, d.chunks); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < d.chunks; i++ {
			// Keyword-reference entries are modelled as they actually are:
			// short and term-dense, so BM25 favours them on incidental token
			// overlap. Without a penalty they would take the top slots.
			kind := "prose"
			text := "This section explains how to configure the universe and " +
				"assign a dmx address to a patched fixture in the show file, " +
				"with worked examples and surrounding narrative " + itoa(i)
			if d.id == "big" && i >= 14 {
				kind = "keyword-reference"
				text = "universe dmx universe dmx " + itoa(i)
			}
			if _, err := raw.Exec(
				`INSERT INTO chunks (doc_id,ordinal,section,heading_path,text,kind,image_count)
				 VALUES (?,?,?,?,?,?,0)`,
				d.id, i, "1."+itoa(i), d.title+" > Section "+itoa(i),
				text, kind); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := raw.Exec(
		`INSERT INTO schema_version (version, applied_at) VALUES (?, datetime('now'))`,
		RequiredSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
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
	var firstKeywordRank int
	for _, r := range def {
		if r.Kind == "keyword-reference" {
			firstKeywordRank = r.Rank
			break
		}
	}
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
	var incFirst int
	for _, r := range inc {
		if r.Kind == "keyword-reference" {
			incFirst = r.Rank
			break
		}
	}
	if incFirst >= firstKeywordRank {
		t.Errorf("include_keyword_reference did not lift them: rank %d vs %d",
			incFirst, firstKeywordRank)
	}
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
