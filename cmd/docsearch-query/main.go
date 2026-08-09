// Command docsearch-query runs an ad-hoc search against an index and prints
// ranked headings. A debugging aid for comparing cold search against the
// oriented (outline then section_filter) path the tools recommend.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/bamsammich/docsearch/internal/store"
)

func main() {
	db := flag.String("db", "var/docsearch.db", "index")
	doc := flag.String("doc", "", "doc_id to scope to")
	section := flag.String("section", "", "section_filter")
	k := flag.Int("k", 5, "results")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: docsearch-query [flags] <query>")
		os.Exit(2)
	}
	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	res, err := st.Search(context.Background(), store.SearchParams{
		Query: flag.Arg(0), DocID: *doc, SectionFilter: *section, K: *k,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(res) == 0 {
		fmt.Println("  no results")
		return
	}
	for _, r := range res {
		hp := r.HeadingPath
		if len(hp) > 66 {
			hp = hp[:66] + "..."
		}
		fmt.Printf("  %d. rel=%.2f img=%-2d %s\n", r.Rank, r.Relevance, r.ImageCount, hp)
	}
}
