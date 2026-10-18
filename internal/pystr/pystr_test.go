package pystr_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// Control and exotic separators are built from code points, so the source
// stays plain ASCII and says which character each case exercises.
var (
	nel     = string(rune(0x85))   // NEXT LINE
	lineSep = string(rune(0x2028)) // LINE SEPARATOR
	ideoSp  = string(rune(0x3000)) // IDEOGRAPHIC SPACE
)

// PystrSuite checks each helper against the Python behaviour it reproduces.
type PystrSuite struct{ suite.Suite }

func TestPystr(t *testing.T) { suite.Run(t, new(PystrSuite)) }

func (s *PystrSuite) TestSplitLinesMatchesPython() {
	// Expected values from Python 3.13's str.splitlines().
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "one line", in: "a", want: []string{"a"}},
		{name: "trailing newline adds nothing", in: "a\n", want: []string{"a"}},
		{name: "lone newline is one empty line", in: "\n", want: []string{""}},
		{name: "blank line kept", in: "a\n\nb", want: []string{"a", "", "b"}},
		{name: "CRLF is one break", in: "a\r\nb", want: []string{"a", "b"}},
		{name: "lone CR breaks", in: "a\rb", want: []string{"a", "b"}},
		{name: "CR then CRLF", in: "a\r\r\nb", want: []string{"a", "", "b"}},
		{name: "vertical tab and form feed", in: "a\vb\fc", want: []string{"a", "b", "c"}},
		{name: "file and group separators", in: "a\x1cb\x1dc", want: []string{"a", "b", "c"}},
		{
			name: "record separator breaks, unit does not",
			in:   "a\x1eb\x1fc",
			want: []string{"a", "b\x1fc"},
		},
		{name: "line separator", in: "a" + lineSep + "b", want: []string{"a", "b"}},
		{name: "next line", in: "a" + nel + "b\n\n", want: []string{"a", "b", ""}},
		{name: "non-ASCII lines", in: "é\nü", want: []string{"é", "ü"}},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, pystr.SplitLines(tt.in))
		})
	}
}

func (s *PystrSuite) TestStripTreatsInformationSeparatorsAsSpace() {
	s.Equal("text", pystr.Strip("\x1c\x1f text "+ideoSp+nel))
}

func (s *PystrSuite) TestSpaceClassAgreesWithIsSpace() {
	re := regexp.MustCompile(`^[` + pystr.SpaceChars + `]$`)
	for r := range rune(0x3100) {
		s.Equal(re.MatchString(string(r)), pystr.IsSpace(r), "U+%04X", r)
	}
}

func (s *PystrSuite) TestIsLetter() {
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		{name: "ASCII letter", r: 'a', want: true},
		{name: "accented letter", r: 'é', want: true},
		{name: "Cyrillic", r: 'Ж', want: true},
		{name: "Han", r: '漢', want: true},
		{name: "Roman numeral, a numeric letter", r: 'Ⅻ', want: true},
		{name: "vulgar fraction, other number", r: '½', want: true},
		{name: "ASCII digit", r: '7', want: false},
		{name: "Arabic-Indic digit", r: '٣', want: false},
		{name: "underscore", r: '_', want: false},
		{name: "space", r: ' ', want: false},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, pystr.IsLetter(tt.r))
		})
	}
}

// ReprSuite holds Repr to what Python's repr() prints. The boilerplate
// finding quotes a line with it, and Go's %q would print double quotes where
// Python prints single ones, on every line without an apostrophe.
type ReprSuite struct{ suite.Suite }

func TestRepr(t *testing.T) { suite.Run(t, new(ReprSuite)) }

func (s *ReprSuite) TestReprMatchesPython() {
	tests := []struct {
		in   string
		want string
		note string
	}{
		{in: "plain", want: `'plain'`},
		{in: "it's", want: `"it's"`, note: "an apostrophe alone switches the quote"},
		{in: `say "hi"`, want: `'say "hi"'`},
		{in: `both ' and "`, want: `'both \' and "'`, note: "both, so the quote is escaped"},
		{in: "tab\there", want: `'tab\there'`},
		{in: "nl\nhere", want: `'nl\nhere'`},
		{in: `back\slash`, want: `'back\\slash'`},
		{in: "café", want: `'café'`, note: "a printable letter is not escaped"},
		{in: "zero\x00byte", want: `'zero\x00byte'`},
		{
			in: "sep" + lineSep + "here",
			// The escape Python writes, spelled so the source holds a
			// backslash rather than the separator itself.
			want: "'sep\\" + "u2028here'",
			note: "a line separator is not printable",
		},
		{in: "😀", want: `'😀'`},
	}
	for _, tt := range tests {
		s.Run(tt.in, func() {
			s.Equal(tt.want, pystr.Repr(tt.in), tt.note)
		})
	}
}
