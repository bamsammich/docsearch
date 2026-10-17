package pystr_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// replacementChar is U+FFFD REPLACEMENT CHARACTER.
var replacementChar = string(rune(0xfffd))

// ReadSuite checks how files are read against Python's Path.read_text and
// PurePath, and SplitLinesKeepEnds against str.splitlines(keepends=True).
type ReadSuite struct{ suite.Suite }

func TestRead(t *testing.T) { suite.Run(t, new(ReadSuite)) }

func (s *ReadSuite) TestReadTextMatchesPython() {
	// Expected values from Python 3.13's
	// TextIOWrapper(BytesIO(b), encoding="utf-8", errors="replace").read().
	tests := []struct {
		name string
		want string
		in   []byte
	}{
		{name: "valid", in: []byte("ok"), want: "ok"},
		{
			name: "truncated three-byte sequence is one replacement",
			in:   []byte("\xe2\x82A"),
			want: replacementChar + "A",
		},
		{name: "lone continuation byte", in: []byte("\x80"), want: replacementChar},
		{name: "byte that never leads", in: []byte("\xff"), want: replacementChar},
		{
			name: "overlong two-byte form",
			in:   []byte("\xc0\xaf"),
			want: replacementChar + replacementChar,
		},
		{
			name: "surrogate",
			in:   []byte("\xed\xa0\x80"),
			want: replacementChar + replacementChar + replacementChar,
		},
		{name: "truncated four-byte sequence", in: []byte("\xf0\x9f\x98"), want: replacementChar},
		{name: "four-byte sequence", in: []byte("\xf0\x9f\x98\x80"), want: string(rune(0x1f600))},
		{
			name: "above U+10FFFF",
			in:   []byte("\xf4\x90\x80\x80"),
			want: replacementChar + replacementChar + replacementChar + replacementChar,
		},
		{
			name: "overlong three-byte form",
			in:   []byte("\xe0\x80\xaf"),
			want: replacementChar + replacementChar + replacementChar,
		},
		{
			name: "overlong four-byte form",
			in:   []byte("\xf0\x80\x80"),
			want: replacementChar + replacementChar + replacementChar,
		},
		{name: "lead byte at the end", in: []byte("\xc2"), want: replacementChar},
		{name: "valid U+FFFD is kept", in: []byte("\xef\xbf\xbd"), want: replacementChar},
		{name: "newlines are universal", in: []byte("a\r\nb\rc\n"), want: "a\nb\nc\n"},
		{name: "replacement before CRLF", in: []byte("\xe2\x82\r\n"), want: replacementChar + "\n"},
		{
			name: "byte order mark is kept",
			in:   []byte("\xef\xbb\xbfbom"),
			want: string(rune(0xfeff)) + "bom",
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, pystr.ReadText(tt.in))
		})
	}
}

func (s *ReadSuite) TestSplitLinesKeepEndsMatchesPython() {
	tests := []struct {
		in   string
		want []string
	}{
		{in: "", want: nil},
		{in: "a", want: []string{"a"}},
		{in: "a\n", want: []string{"a\n"}},
		{in: "a\r\nb", want: []string{"a\r\n", "b"}},
		{in: "a\x0cb" + lineSep, want: []string{"a\x0c", "b" + lineSep}},
		{in: "a\n\nb\n", want: []string{"a\n", "\n", "b\n"}},
	}
	for _, tt := range tests {
		s.Run(tt.in, func() {
			s.Equal(tt.want, pystr.SplitLinesKeepEnds(tt.in))
		})
	}
}

func (s *ReadSuite) TestStemAndSuffixMatchPurePath() {
	// Expected values from Python 3.13's PurePath(name).stem and .suffix.
	tests := []struct {
		name, stem, suffix string
	}{
		{name: "guide.md", stem: "guide", suffix: ".md"},
		{name: "a.tar.gz", stem: "a.tar", suffix: ".gz"},
		{name: ".bashrc", stem: ".bashrc", suffix: ""},
		{name: "notes.", stem: "notes.", suffix: ""},
		{name: "x", stem: "x", suffix: ""},
		{name: "..", stem: "..", suffix: ""},
		{name: "a..b", stem: "a.", suffix: ".b"},
		{name: ".a.b", stem: ".a", suffix: ".b"},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.stem, pystr.Stem(tt.name))
			s.Equal(tt.suffix, pystr.Suffix(tt.name))
		})
	}
}
