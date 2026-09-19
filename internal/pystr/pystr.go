// Package pystr reproduces the Python string semantics the ingest pipeline
// depends on, so Go code ported from it produces identical output.
//
// Each definition here was checked by enumerating every code point in Python
// 3.13 (Unicode 15.1). Go 1.26 ships Unicode 15.0, so characters added in 15.1
// may classify differently; nothing else does.
package pystr

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SpaceChars is the body of a regexp character class matching exactly what
// Python's re `\s` and str.isspace accept. Go's `\s` is ASCII only, and
// unicode.IsSpace omits the four information separators \x1c to \x1f.
const SpaceChars = `\t\n\x0b\x0c\r\x1c-\x1f \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// WordChars is the body of a class matching Python's `\w` on str patterns:
// every letter, every number and the underscore. Go's `\w` is ASCII only.
const WordChars = `\p{L}\p{N}_`

// spaceTable holds the code points Python's str.isspace accepts.
var spaceTable = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x0009, Hi: 0x000d, Stride: 1},
		{Lo: 0x001c, Hi: 0x0020, Stride: 1},
		{Lo: 0x0085, Hi: 0x0085, Stride: 1},
		{Lo: 0x00a0, Hi: 0x00a0, Stride: 1},
		{Lo: 0x1680, Hi: 0x1680, Stride: 1},
		{Lo: 0x2000, Hi: 0x200a, Stride: 1},
		{Lo: 0x2028, Hi: 0x2029, Stride: 1},
		{Lo: 0x202f, Hi: 0x202f, Stride: 1},
		{Lo: 0x205f, Hi: 0x205f, Stride: 1},
		{Lo: 0x3000, Hi: 0x3000, Stride: 1},
	},
	LatinOffset: 4,
}

// lineBreakTable holds the code points Python's str.splitlines splits at.
var lineBreakTable = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x000a, Hi: 0x000d, Stride: 1},
		{Lo: 0x001c, Hi: 0x001e, Stride: 1},
		{Lo: 0x0085, Hi: 0x0085, Stride: 1},
		{Lo: 0x2028, Hi: 0x2029, Stride: 1},
	},
	LatinOffset: 3,
}

// IsSpace reports whether Python's str.isspace would accept r.
func IsSpace(r rune) bool {
	return unicode.Is(spaceTable, r)
}

// IsWord reports whether Python's `\w` would match r.
func IsWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_'
}

// IsLetter reports whether Python's `[^\W\d_]` would match r: a letter, or a
// number that is not a decimal digit, such as the Roman numeral Ⅻ.
func IsLetter(r rune) bool {
	return unicode.IsLetter(r) || unicode.In(r, unicode.Nl, unicode.No)
}

// Strip is Python's str.strip().
func Strip(s string) string {
	return strings.TrimFunc(s, IsSpace)
}

// SplitLines is Python's str.splitlines(): it splits at \n, \r, \r\n, \v, \f,
// \x1c, \x1d, \x1e, \x85,   and  , and a trailing separator does not
// produce an empty last element.
func SplitLines(s string) []string {
	var out []string
	for s != "" {
		at, width := firstLineBreak(s)
		if at < 0 {
			return append(out, s)
		}
		out = append(out, s[:at])
		s = s[at+width:]
	}
	return out
}

// SplitLinesKeepEnds is Python's str.splitlines(keepends=True): SplitLines
// with each line's separator left on its end.
func SplitLinesKeepEnds(s string) []string {
	var out []string
	for s != "" {
		at, width := firstLineBreak(s)
		if at < 0 {
			return append(out, s)
		}
		out = append(out, s[:at+width])
		s = s[at+width:]
	}
	return out
}

// firstLineBreak returns the byte offset and width of the first line break in
// s, treating \r\n as one break, or -1 when there is none.
func firstLineBreak(s string) (at, width int) {
	for i, r := range s {
		if !unicode.Is(lineBreakTable, r) {
			continue
		}
		if r == '\r' && strings.HasPrefix(s[i+1:], "\n") {
			return i, 2
		}
		return i, utf8.RuneLen(r)
	}
	return -1, 0
}
