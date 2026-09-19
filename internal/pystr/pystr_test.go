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
	_nel     = string(rune(0x85))   // NEXT LINE
	_lineSep = string(rune(0x2028)) // LINE SEPARATOR
	_ideoSp  = string(rune(0x3000)) // IDEOGRAPHIC SPACE
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
		{name: "line separator", in: "a" + _lineSep + "b", want: []string{"a", "b"}},
		{name: "next line", in: "a" + _nel + "b\n\n", want: []string{"a", "b", ""}},
		{name: "non-ASCII lines", in: "é\nü", want: []string{"é", "ü"}},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, pystr.SplitLines(tt.in))
		})
	}
}

func (s *PystrSuite) TestStripTreatsInformationSeparatorsAsSpace() {
	s.Equal("text", pystr.Strip("\x1c\x1f text "+_ideoSp+_nel))
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
