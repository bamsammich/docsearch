package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgstore"
)

// SelfLabelSuite covers the probe query generated from a sampled chunk.
type SelfLabelSuite struct{ suite.Suite }

func TestSelfLabel(t *testing.T) { suite.Run(t, new(SelfLabelSuite)) }

// The full path names every ancestor, so querying with it would match on
// chapter vocabulary rather than on the section under test.
func (s *SelfLabelSuite) TestGeneratedQueryUsesTheLeafHeadingNotTheWholeAncestry() {
	q := generateQuery(pgstore.SampledChunk{
		HeadingPath: "Manual > Playback > Executors > Assigning Sequences",
		Text:        "Press Assign then select the sequence to attach it to an executor.",
	})
	s.Require().True(strings.HasPrefix(q, "Assigning Sequences"), "query %q leads with the leaf", q)
	s.NotContains(q, "Playback", "no ancestor heading")
}

// A query containing the whole chunk tests string equality, not retrieval.
func (s *SelfLabelSuite) TestGeneratedQueryIsNotACopyOfTheChunk() {
	body := strings.Repeat("configuration parameter adjustment procedure sequence ", 40)
	q := generateQuery(pgstore.SampledChunk{HeadingPath: "Setup", Text: body})
	s.LessOrEqual(len(strings.Fields(q)), selfLabelTerms+4, "a short probe: %q", q)
}

func (s *SelfLabelSuite) TestGeneratedQueryPrefersDistinctiveTerms() {
	q := generateQuery(pgstore.SampledChunk{
		HeadingPath: "Setup",
		Text:        "the of and a to synchronisation calibration threshold",
	})
	for _, want := range []string{"synchronisation", "calibration"} {
		s.Contains(q, want)
	}
	for _, unwanted := range []string{" the ", " and ", " of "} {
		s.NotContains(q, unwanted)
	}
}

// Counted as unprobeable rather than scored as a miss: a chunk of pure
// stopwords says something about extraction, not about ranking.
func (s *SelfLabelSuite) TestAChunkWithNoDistinctiveTextYieldsAnUnusableQuery() {
	q := generateQuery(pgstore.SampledChunk{HeadingPath: "", Text: "the and of to in"})
	s.Less(len(wordsOf(q)), 2, "query %q is too thin to probe", q)
}

func (s *SelfLabelSuite) TestHeadingOnlyChunkStillProducesAQuery() {
	q := generateQuery(pgstore.SampledChunk{HeadingPath: "Patching Fixtures", Text: ""})
	s.Contains(q, "Patching")
}
