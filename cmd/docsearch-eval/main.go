// Command docsearch-eval runs the committed retrieval query set against a
// built index and reports hit rates.
//
// It drives the real store.Search path, not a reimplementation, so what it
// measures is what the MCP server returns. Misses are printed in full: a query
// that fails is information about whether the retrieval design holds, and the
// rule for this suite is that a failing query stays in the file unedited.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/bamsammich/docsearch/internal/store"
)

type query struct {
	ID       string  `json:"id"`
	Doc      string  `json:"doc"`
	Expect   *string `json:"expect"`
	Category string  `json:"category"`
	Blind    bool    `json:"blind"`
	Query    string  `json:"query"`
	Note     string  `json:"note"`
}

type file struct {
	Queries []query `json:"queries"`
}

var docIDs = map[string]string{
	"grandma2": "manual-of-ma-lighting-international-gmbh",
	"qlcplus":  "q-light-controller-plus-user-documentation",
}

// matches applies the expectation semantics declared in queries.json: a
// section subtree for the numbered corpus, a heading substring for the
// unnumbered one.
func matches(q query, r store.SearchResult) bool {
	if q.Expect == nil {
		return false
	}
	want := *q.Expect
	if q.Doc == "grandma2" {
		return store.SectionCovers(want, r.Section)
	}
	return strings.Contains(strings.ToLower(r.HeadingPath), strings.ToLower(want))
}

func main() {
	dbPath := flag.String("db", "var/docsearch.db", "index to evaluate")
	qPath := flag.String("queries", "tests/retrieval/queries.json", "committed query set")
	dump := flag.String("dump-candidates", "", "write top-N BM25 candidates as JSON here")
	dumpN := flag.Int("dump-n", 50, "candidates per query when dumping")
	flag.Parse()

	raw, err := os.ReadFile(*qPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	type outcome struct {
		q      query
		top1   bool
		top3   bool
		hits   []store.SearchResult
		hitPos int
	}
	var results []outcome

	for _, q := range f.Queries {
		if q.Category == "cross-doc" {
			continue
		}
		res, err := st.Search(ctx, store.SearchParams{
			Query: q.Query, DocID: docIDs[q.Doc], K: 5,
		})
		if err != nil {
			fmt.Printf("%s ERROR: %v\n", q.ID, err)
			continue
		}
		o := outcome{q: q, hits: res, hitPos: -1}
		for i, r := range res {
			if matches(q, r) {
				o.hitPos = i + 1
				break
			}
		}
		o.top1 = o.hitPos == 1
		o.top3 = o.hitPos >= 1 && o.hitPos <= 3
		results = append(results, o)
	}

	if *dump != "" {
		// Emitted from the same Search path the server uses, so a reranking
		// experiment operates on exactly what production retrieves rather than
		// on a reimplementation that could quietly diverge.
		dumpCandidates(ctx, st, f.Queries, *dump, *dumpN)
		return
	}

	fmt.Println("========================================================================")
	fmt.Println("RETRIEVAL EVALUATION")
	fmt.Println("========================================================================")
	fmt.Printf("%-5s %-6s %-18s %-5s %s\n", "ID", "HIT@", "CATEGORY", "BLIND", "QUERY")
	for _, o := range results {
		hit := "MISS"
		if o.hitPos > 0 {
			hit = fmt.Sprintf("%d", o.hitPos)
		}
		blind := "yes"
		if !o.q.Blind {
			blind = "NO"
		}
		fmt.Printf("%-5s %-6s %-18s %-5s %s\n", o.q.ID, hit, o.q.Category, blind, o.q.Query)
	}

	summarise := func(label string, keep func(outcome) bool) {
		var n, t1, t3 int
		for _, o := range results {
			if !keep(o) {
				continue
			}
			n++
			if o.top1 {
				t1++
			}
			if o.top3 {
				t3++
			}
		}
		if n == 0 {
			return
		}
		fmt.Printf("  %-28s n=%-3d top-1 %2d/%-2d (%3.0f%%)   top-3 %2d/%-2d (%3.0f%%)\n",
			label, n, t1, n, 100*float64(t1)/float64(n), t3, n, 100*float64(t3)/float64(n))
	}

	fmt.Println("\n--- hit rates ---")
	summarise("ALL", func(outcome) bool { return true })
	summarise("blind only", func(o outcome) bool { return o.q.Blind })
	summarise("informed by this session", func(o outcome) bool { return !o.q.Blind })

	fmt.Println("\n--- by category ---")
	cats := map[string]bool{}
	for _, o := range results {
		cats[o.q.Category] = true
	}
	var names []string
	for c := range cats {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		summarise(c, func(o outcome) bool { return o.q.Category == c })
	}

	fmt.Println("\n--- by document ---")
	for _, d := range []string{"grandma2", "qlcplus"} {
		summarise(d, func(o outcome) bool { return o.q.Doc == d })
	}

	crossDoc(ctx, st, []string{
		"dmx universe addressing",
		"chaser",
		"fixture",
	})

	fmt.Println("\n========================================================================")
	fmt.Println("MISSES IN FULL (these stay in the query set unedited)")
	fmt.Println("========================================================================")
	for _, o := range results {
		if o.top3 {
			continue
		}
		fmt.Printf("\n%s [%s] %q\n  expected: %v\n", o.q.ID, o.q.Category, o.q.Query,
			derefOr(o.q.Expect, "-"))
		if o.q.Note != "" {
			fmt.Printf("  note: %s\n", o.q.Note)
		}
		for i, r := range o.hits {
			if i >= 3 {
				break
			}
			fmt.Printf("   %d. rel=%.2f sec=%-8s img=%-2d %s\n",
				i+1, r.Relevance, orDash(r.Section), r.ImageCount, truncate(r.HeadingPath, 72))
		}
	}
}

// crossDoc runs identical queries scoped and unscoped to expose the effect the
// spec flags: IDF is computed over the whole table, so a term that is rare
// globally but common inside a small document can lift that document's chunks
// above better answers in a large one.
func crossDoc(ctx context.Context, st *store.Store, queries []string) {
	fmt.Println("\n========================================================================")
	fmt.Println("CROSS-DOCUMENT IDF EFFECT")
	fmt.Println("========================================================================")
	fmt.Println("Corpus sizes are deliberately lopsided: grandMA2 944 chunks, QLC+ 265.")

	for _, q := range queries {
		fmt.Printf("\n--- %q ---\n", q)
		unscoped, err := st.Search(ctx, store.SearchParams{Query: q, K: 6})
		if err != nil {
			fmt.Println("  error:", err)
			continue
		}
		counts := map[string]int{}
		fmt.Println("  UNSCOPED:")
		for _, r := range unscoped {
			counts[r.DocID]++
			fmt.Printf("    %d. rel=%.2f [%-9s] %s\n", r.Rank, r.Relevance,
				shortDoc(r.DocID), truncate(r.HeadingPath, 62))
		}
		small := counts["q-light-controller-plus-user-documentation"]
		share := 100 * float64(small) / float64(len(unscoped))
		fmt.Printf("  -> QLC+ holds %d/%d unscoped slots (%.0f%%) while being %.0f%% of the index\n",
			small, len(unscoped), share, 100*265.0/(944.0+265.0))

		for _, d := range []string{"manual-of-ma-lighting-international-gmbh",
			"q-light-controller-plus-user-documentation"} {
			scoped, err := st.Search(ctx, store.SearchParams{Query: q, DocID: d, K: 2})
			if err != nil || len(scoped) == 0 {
				continue
			}
			fmt.Printf("  SCOPED %-9s top: rel=%.2f %s\n", shortDoc(d), scoped[0].Relevance,
				truncate(scoped[0].HeadingPath, 60))
		}
	}
}

func shortDoc(id string) string {
	if strings.HasPrefix(id, "q-light") {
		return "QLC+"
	}
	return "grandMA2"
}

type candidate struct {
	ChunkID     int64   `json:"chunk_id"`
	Section     string  `json:"section"`
	HeadingPath string  `json:"heading_path"`
	Kind        string  `json:"kind"`
	ImageCount  int     `json:"image_count"`
	Relevance   float64 `json:"relevance"`
	Text        string  `json:"text"`
}

type dumpEntry struct {
	ID         string      `json:"id"`
	Query      string      `json:"query"`
	Doc        string      `json:"doc"`
	Expect     *string     `json:"expect"`
	Category   string      `json:"category"`
	BM25Hit    int         `json:"bm25_hit_position"`
	Candidates []candidate `json:"candidates"`
}

func dumpCandidates(ctx context.Context, st *store.Store, queries []query,
	path string, n int) {
	var out []dumpEntry
	for _, q := range queries {
		if q.Category == "cross-doc" {
			continue
		}
		res, err := st.Search(ctx, store.SearchParams{
			Query: q.Query, DocID: docIDs[q.Doc], K: n,
		})
		if err != nil {
			continue
		}
		e := dumpEntry{ID: q.ID, Query: q.Query, Doc: q.Doc, Expect: q.Expect,
			Category: q.Category, BM25Hit: -1}
		for i, r := range res {
			if matches(q, r) && e.BM25Hit < 0 {
				e.BM25Hit = i + 1
			}
			e.Candidates = append(e.Candidates, candidate{
				ChunkID: r.ChunkID, Section: r.Section, HeadingPath: r.HeadingPath,
				Kind: r.Kind, ImageCount: r.ImageCount, Relevance: r.Relevance, Text: r.Text,
			})
		}
		out = append(out, e)
	}
	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d queries, up to %d candidates each, to %s\n", len(out), n, path)
}

func derefOr(s *string, alt string) string {
	if s == nil {
		return alt
	}
	return *s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
