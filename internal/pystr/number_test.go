package pystr_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// Code points spelled out, so the source stays plain ASCII.
func runes(cps ...rune) string { return string(cps) }

// NumberSuite checks the numeric and case helpers against Python 3.13.
type NumberSuite struct{ suite.Suite }

func TestNumber(t *testing.T) { suite.Run(t, new(NumberSuite)) }

func (s *NumberSuite) TestRoundMatchesPython() {
	// Expected values from Python's round(x, 1) and round(x, 3).
	tests := []struct {
		x          float64
		one, three float64
	}{
		{x: 2.675, one: 2.7, three: 2.675},
		{x: 0.05, one: 0.1, three: 0.05},
		{x: 0.15, one: 0.1, three: 0.15},
		{x: 0.25, one: 0.2, three: 0.25},
		{x: 100.25, one: 100.2, three: 100.25},
		{x: 17.349999, one: 17.3, three: 17.35},
		{x: 1e-7, one: 0, three: 0},
		{x: 791.9500000000001, one: 792, three: 791.95},
	}
	for _, tt := range tests {
		// Exact: a rounding difference is exactly what these catch.
		s.InDelta(tt.one, pystr.Round(tt.x, 1), 0, "round(%v, 1)", tt.x)
		s.InDelta(tt.three, pystr.Round(tt.x, 3), 0, "round(%v, 3)", tt.x)
	}
	s.True(math.Signbit(pystr.Round(-0.04, 1)), "round(-0.04, 1) is -0.0")
}

func (s *NumberSuite) TestFloorDivMatchesPython() {
	tests := []struct{ x, y, want float64 }{
		{x: 7.9, y: 4, want: 1},
		{x: 8, y: 4, want: 2},
		{x: -0.5, y: 4, want: -1},
		{x: 403.99999999999994, y: 4, want: 100},
		{x: 1e-300, y: 4, want: 0},
		{x: -8, y: 4, want: -2},
	}
	for _, tt := range tests {
		s.InDelta(tt.want, pystr.FloorDiv(tt.x, tt.y), 0, "%v // %v", tt.x, tt.y)
	}
}

func (s *NumberSuite) TestAtoiReadsDecimalDigitsInAnyScript() {
	tests := []struct {
		in   string
		want int
	}{
		{in: "7", want: 7},
		{in: "007", want: 7},
		{in: runes(0x0663), want: 3},            // ARABIC-INDIC DIGIT THREE
		{in: runes(0xff11, 0xff12), want: 12},   // FULLWIDTH DIGITS ONE, TWO
		{in: runes(0x1d7d7, 0x1d7d7), want: 99}, // MATHEMATICAL BOLD DIGIT NINE, in abutting runs
		{in: runes(0x1d7e1), want: 9},           // MATHEMATICAL DOUBLE-STRUCK DIGIT NINE
	}
	for _, tt := range tests {
		got, err := pystr.Atoi(tt.in)
		s.Require().NoError(err, "%+q", tt.in)
		s.Equal(tt.want, got, "%+q", tt.in)
	}
	for _, bad := range []string{"", "1a", runes(0x00b2)} { // SUPERSCRIPT TWO is not decimal
		_, err := pystr.Atoi(bad)
		s.Require().ErrorIs(err, pystr.ErrNotDigits, "%+q", bad)
		s.False(pystr.IsDigits(bad))
	}
}

func (s *NumberSuite) TestLowerMatchesPython() {
	sigma, alpha := rune(0x03a3), rune(0x0391)
	tests := []struct {
		in, want string
	}{
		{in: "HEADING 1", want: "heading 1"},
		// "ΟΔΟΣ" ends in a final sigma; a lone "Σ" and "ΣΑ" do not.
		{in: runes(0x039f, 0x0394, 0x039f, sigma), want: runes(0x03bf, 0x03b4, 0x03bf, 0x03c2)},
		{in: runes(sigma, alpha), want: runes(0x03c3, 0x03b1)},
		{in: runes(sigma), want: runes(0x03c3)},
		// Case-ignorable punctuation is skipped on both sides.
		{in: "A." + runes(sigma) + ".", want: "a." + runes(0x03c2) + "."},
		{in: "A" + runes(sigma) + "'", want: "a" + runes(0x03c2) + "'"},
		{in: "Heading " + runes(sigma, sigma), want: "heading " + runes(0x03c3, 0x03c2)},
		// CAPITAL I WITH DOT ABOVE becomes "i" and COMBINING DOT ABOVE.
		{in: runes(0x0130) + "stanbul", want: "i" + runes(0x0307) + "stanbul"},
		{in: runes(0x1e9e), want: runes(0x00df)}, // CAPITAL SHARP S
	}
	for _, tt := range tests {
		s.Equal(tt.want, pystr.Lower(tt.in), "%+q", tt.in)
	}
}
