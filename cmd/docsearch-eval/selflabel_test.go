package main

import (
	"strings"
	"testing"

	"github.com/bamsammich/docsearch/internal/store"
)

func TestGeneratedQueryUsesTheLeafHeadingNotTheWholeAncestry(t *testing.T) {
	// The full path names every ancestor, so querying with it would match on
	// chapter vocabulary rather than on the section under test.
	c := store.SampledChunk{
		HeadingPath: "Manual > Playback > Executors > Assigning Sequences",
		Text:        "Press Assign then select the sequence to attach it to an executor.",
	}
	q := generateQuery(c)
	if !strings.HasPrefix(q, "Assigning Sequences") {
		t.Fatalf("query %q does not lead with the leaf heading", q)
	}
	if strings.Contains(q, "Playback") {
		t.Errorf("query %q pulled in an ancestor heading", q)
	}
}

func TestGeneratedQueryIsNotACopyOfTheChunk(t *testing.T) {
	// A query containing the whole chunk tests string equality, not retrieval.
	body := strings.Repeat("configuration parameter adjustment procedure sequence ", 40)
	q := generateQuery(store.SampledChunk{HeadingPath: "Setup", Text: body})
	if got := len(strings.Fields(q)); got > selfLabelTerms+4 {
		t.Errorf("query has %d words, want a short probe: %q", got, q)
	}
}

func TestGeneratedQueryPrefersDistinctiveTerms(t *testing.T) {
	c := store.SampledChunk{
		HeadingPath: "Setup",
		Text:        "the of and a to synchronisation calibration threshold",
	}
	q := generateQuery(c)
	for _, want := range []string{"synchronisation", "calibration"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q dropped the distinctive term %q", q, want)
		}
	}
	for _, unwanted := range []string{" the ", " and ", " of "} {
		if strings.Contains(q, unwanted) {
			t.Errorf("query %q kept the stopword %q", q, unwanted)
		}
	}
}

func TestAChunkWithNoDistinctiveTextYieldsAnUnusableQuery(t *testing.T) {
	// Counted as unprobeable rather than scored as a miss: a chunk of pure
	// stopwords says something about extraction, not about ranking.
	q := generateQuery(store.SampledChunk{HeadingPath: "", Text: "the and of to in"})
	if len(wordsOf(q)) >= 2 {
		t.Errorf("query %q should be too thin to probe", q)
	}
}

func TestHeadingOnlyChunkStillProducesAQuery(t *testing.T) {
	q := generateQuery(store.SampledChunk{HeadingPath: "Patching Fixtures", Text: ""})
	if !strings.Contains(q, "Patching") {
		t.Errorf("query %q lost the heading", q)
	}
}
