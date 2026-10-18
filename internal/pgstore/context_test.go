package pgstore

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

// TrimSuite pins how a context span is cut to the token cap.
type TrimSuite struct{ suite.Suite }

func TestTrim(t *testing.T) { suite.Run(t, new(TrimSuite)) }

// span is chunks at ordinals 0 to n-1, each about size tokens.
func span(n, size int) []ContextChunk {
	out := make([]ContextChunk, n)
	for i := range out {
		out[i] = ContextChunk{Ordinal: i, Text: strings.Repeat("word ", size)}
	}
	return out
}

func ordinals(chunks []ContextChunk) []int {
	out := make([]int, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, c.Ordinal)
	}
	return out
}

func (s *TrimSuite) TestASpanUnderTheCapIsKeptWhole() {
	got, truncated := trimToCap(span(3, 10), 1)
	s.False(truncated)
	s.Equal([]int{0, 1, 2}, ordinals(got))
}

// Chunks before the anchor go first, then those after it, and the anchor
// itself always stays.
func (s *TrimSuite) TestTheFrontGoesFirstAndTheAnchorStays() {
	chunks := span(9, 1)
	size := estimateTokens(chunks[0].Text)
	perChunk := contextTokenCap/size/3 + 1
	got, truncated := trimToCap(span(9, perChunk), 4)
	s.True(truncated)
	s.Contains(ordinals(got), 4)
	s.Equal(4, got[0].Ordinal, "everything before the anchor went first")
	s.LessOrEqual(estimateSpanOf(got), contextTokenCap)
}

func (s *TrimSuite) TestAnAnchorPastTheCapOnItsOwnIsStillReturned() {
	got, truncated := trimToCap(span(3, contextTokenCap*2), 1)
	s.True(truncated)
	s.Equal([]int{1}, ordinals(got))
}

func (s *TrimSuite) TestAnEmptySpanIsNotTruncated() {
	got, truncated := trimToCap(nil, 0)
	s.False(truncated)
	s.Empty(got)
}

func estimateSpanOf(chunks []ContextChunk) int {
	total := 0
	for _, c := range chunks {
		total += estimateTokens(c.Text)
	}
	return total
}
