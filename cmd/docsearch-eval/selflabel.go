package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bamsammich/docsearch/internal/store"
)

// Self-labelled retrieval probe.
//
// Measuring retrieval otherwise requires a query set with expected sections
// recorded before the queries are run. Authoring one took two passes and a
// documented contamination disclosure, so nobody else will do it, and without
// it nobody else can tell whether retrieval works on their own documents.
//
// This builds a query out of a chunk and asks whether that chunk comes back.
// The chunk is its own label, so no authoring is needed and the probe runs on
// any corpus the moment it is ingested.

// selfLabelStride draws roughly this many chunks from each document. Enough to
// separate a working index from a broken one without running a query per chunk
// on a 1,000-chunk manual.
const selfLabelSample = 60

// selfLabelTerms is how many body terms join the heading in a generated query.
// Few enough that the query is a plausible question rather than a copy of the
// chunk, which would test string equality rather than retrieval.
const selfLabelTerms = 4

// generateQuery builds a query from a chunk's own heading and body.
//
// The leaf heading plus a few of the longest body terms approximates a caller
// who knows roughly what a section is called and remembers a distinctive word
// from it. Longest-first because long terms are the most discriminating ones
// BM25 has to work with; drawing common short words would measure little.
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
	if len(terms) > selfLabelTerms {
		terms = terms[:selfLabelTerms]
	}
	return strings.TrimSpace(leaf + " " + strings.Join(terms, " "))
}

type selfLabelResult struct {
	docID   string
	total   int
	skipped int
	hits    map[int]int // depth -> count
}

func runSelfLabel(ctx context.Context, st *store.Store) {
	fmt.Println("========================================================================")
	fmt.Println("SELF-LABELLED RETRIEVAL PROBE")
	fmt.Println("========================================================================")
	fmt.Println("Each query is built from a chunk's own heading and body, and scored on")
	fmt.Println("whether that chunk comes back.")
	fmt.Println()
	fmt.Println("WHAT THIS MEASURES: lexical round-trip. Can the index find a chunk using")
	fmt.Println("words the chunk itself contains. A low score means something is broken --")
	fmt.Println("structure collapsed, boilerplate dominating the term mass, chunk sizes in")
	fmt.Println("the wrong unit, or section filters that do not narrow.")
	fmt.Println()
	fmt.Println("WHAT IT DOES NOT MEASURE, and these matter:")
	fmt.Println("  - The vocabulary gap, the hard case. A caller asking \"strike the lamps\"")
	fmt.Println("    about a section that says \"Lamp On\" shares no terms with its answer,")
	fmt.Println("    and no self-labelled query ever will.")
	fmt.Println("  - Whether the structure is any good. A document cut into fixed windows")
	fmt.Println("    scores 100% here: every slice still holds distinctive words. It is")
	fmt.Println("    `docsearch verify` that reports such a document unusable, because")
	fmt.Println("    consecutive slices share one heading and section_filter cannot")
	fmt.Println("    separate them. A full score here is not a clean bill of health.")

	docs, err := st.ListDocuments(ctx)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	depths := []int{1, 3, 8, 20}
	var results []selfLabelResult

	for _, d := range docs {
		total := 0
		if d.ChunkCount != nil {
			total = *d.ChunkCount
		}
		stride := 1
		if total > selfLabelSample {
			stride = total / selfLabelSample
		}
		sample, err := st.SampleChunks(ctx, d.DocID, stride)
		if err != nil {
			fmt.Printf("\n%s: error: %v\n", d.DocID, err)
			continue
		}
		res := selfLabelResult{docID: d.DocID, hits: map[int]int{}}
		for _, c := range sample {
			q := generateQuery(c)
			// A chunk with too little distinctive text to form a query cannot
			// be probed. Counted rather than dropped: a corpus where many
			// chunks are unprobeable is itself the finding.
			if len(wordsOf(q)) < 2 {
				res.skipped++
				continue
			}
			res.total++
			hits, err := st.Search(ctx, store.SearchParams{
				Query: q, DocID: d.DocID, K: 20,
			})
			if err != nil {
				continue
			}
			for i, h := range hits {
				if h.ChunkID == c.ChunkID {
					for _, k := range depths {
						if i+1 <= k {
							res.hits[k]++
						}
					}
					break
				}
			}
		}
		results = append(results, res)
	}

	fmt.Println("\n--- round-trip recall ---")
	fmt.Printf("  %-44s %5s %6s %6s %6s %6s\n", "document", "n", "@1", "@3", "@8", "@20")
	for _, r := range results {
		if r.total == 0 {
			fmt.Printf("  %-44s %5d  no probeable chunks\n", trunc(r.docID, 44), 0)
			continue
		}
		pct := func(k int) string {
			return fmt.Sprintf("%.0f%%", 100*float64(r.hits[k])/float64(r.total))
		}
		fmt.Printf("  %-44s %5d %6s %6s %6s %6s\n",
			trunc(r.docID, 44), r.total, pct(1), pct(3), pct(8), pct(20))
		if r.skipped > 0 {
			fmt.Printf("  %-44s       %d chunk(s) had too little distinctive text to probe\n",
				"", r.skipped)
		}
	}

	fmt.Println()
	for _, r := range results {
		if r.total == 0 {
			continue
		}
		at8 := 100 * float64(r.hits[8]) / float64(r.total)
		switch {
		case at8 >= 90:
			fmt.Printf("  %s: round-trip OK. Chunks are findable by their own\n"+
				"    vocabulary. This says nothing about whether the structure is usable --\n"+
				"    run `docsearch verify %s` for that.\n",
				trunc(r.docID, 44), r.docID)
		case at8 >= 70:
			fmt.Printf("  %s: weak. Some chunks cannot be found by their own words;\n"+
				"    look at chunk sizes and at how much of the text is repeated furniture.\n",
				trunc(r.docID, 44))
		default:
			fmt.Printf("  %s: BROKEN. Most chunks cannot be retrieved using words they\n"+
				"    themselves contain. Run `docsearch verify %s` -- this is a\n"+
				"    structural failure, not a ranking one.\n",
				trunc(r.docID, 44), r.docID)
		}
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
