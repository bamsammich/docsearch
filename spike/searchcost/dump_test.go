//go:build spike

package searchcost

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/bamsammich/docsearch/internal/pgstore"
)

// TestDumpUnscoped writes every query of a query set's unscoped results, so
// two builds can be diffed against one database:
//
//	SEARCHCOST_DSN=... SEARCHCOST_QUERIES=tests/retrieval/sites.json SEARCHCOST_OUT=a.json \
//	  go test -tags spike -run TestDumpUnscoped ./spike/searchcost/
func TestDumpUnscoped(t *testing.T) {
	dsn, path, out := os.Getenv("SEARCHCOST_DSN"), os.Getenv("SEARCHCOST_QUERIES"), os.Getenv("SEARCHCOST_OUT")
	if dsn == "" || path == "" || out == "" {
		t.Skip("set SEARCHCOST_DSN, SEARCHCOST_QUERIES and SEARCHCOST_OUT")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var set struct {
		Queries []struct {
			ID    string `json:"id"`
			Query string `json:"query"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatal(err)
	}
	st, err := pgstore.Open(dsn, "default")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	lines := map[string][]string{}
	var took time.Duration
	for _, q := range set.Queries {
		for _, p := range []pgstore.SearchParams{
			{Query: q.Query, K: 8},
			{Query: q.Query, K: 20},
			{Query: q.Query, K: 8, IncludeKeywordReference: true},
		} {
			start := time.Now()
			res, err := st.Search(t.Context(), p)
			took += time.Since(start)
			if err != nil {
				t.Fatalf("%s: %v", q.ID, err)
			}
			key := fmt.Sprintf("%s k=%d kw=%v", q.ID, p.K, p.IncludeKeywordReference)
			for _, r := range res {
				lines[key] = append(lines[key], fmt.Sprintf("%d %s %d %.6f %v",
					r.Rank, r.DocID, r.ChunkID, r.Relevance, r.IndexBoost))
			}
		}
	}
	b, err := json.MarshalIndent(lines, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d searches in %s", len(lines), took)
}
