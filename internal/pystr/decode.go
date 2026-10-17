package pystr

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// ReadFile is Python's Path(path).read_text(encoding="utf-8",
// errors="replace"): the file at path, decoded by ReadText.
func ReadFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return ReadText(raw), nil
}

// ReadText is what Python's Path.read_text(encoding="utf-8", errors="replace")
// returns for a file's bytes: the text decoded as UTF-8, then "\r\n" and a
// lone "\r" each read as "\n".
//
// Invalid bytes become U+FFFD the way Python replaces them, one per maximal
// subpart: a truncated sequence such as "\xe2\x82" is one replacement, where
// ranging over the bytes in Go would give two.
func ReadText(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n == 1 {
			n = invalidPrefix(b)
		}
		sb.WriteRune(r)
		b = b[n:]
	}
	return strings.ReplaceAll(strings.ReplaceAll(sb.String(), "\r\n", "\n"), "\r", "\n")
}

// invalidPrefix returns how many bytes at the start of b Python's decoder
// replaces with one U+FFFD. b starts with an invalid sequence: either a byte
// that cannot lead one, which is replaced alone, or a lead byte followed by
// fewer valid continuation bytes than it needs, which is replaced together
// with them.
func invalidPrefix(b []byte) int {
	seq := leadSequence(b[0])
	lo, hi := seq.lo, seq.hi
	n := 1
	for n <= seq.need && n < len(b) && b[n] >= lo && b[n] <= hi {
		// Only the byte after the lead has a narrower range.
		lo, hi = 0x80, 0xbf
		n++
	}
	return n
}

// wellFormed is one row of Unicode Table 3-7, "Well-Formed UTF-8 Byte
// Sequences": lead bytes from..to take a second byte in lo..hi and need
// continuation bytes in all.
type wellFormed struct {
	need             int
	from, to, lo, hi byte
}

var utf8Sequences = []wellFormed{
	{from: 0xc2, to: 0xdf, lo: 0x80, hi: 0xbf, need: 1},
	{from: 0xe0, to: 0xe0, lo: 0xa0, hi: 0xbf, need: 2},
	{from: 0xe1, to: 0xec, lo: 0x80, hi: 0xbf, need: 2},
	{from: 0xed, to: 0xed, lo: 0x80, hi: 0x9f, need: 2},
	{from: 0xee, to: 0xef, lo: 0x80, hi: 0xbf, need: 2},
	{from: 0xf0, to: 0xf0, lo: 0x90, hi: 0xbf, need: 3},
	{from: 0xf1, to: 0xf3, lo: 0x80, hi: 0xbf, need: 3},
	{from: 0xf4, to: 0xf4, lo: 0x80, hi: 0x8f, need: 3},
}

// leadSequence is the row lead begins; a byte that cannot lead a sequence
// gets a row needing no continuation bytes.
func leadSequence(lead byte) wellFormed {
	for _, row := range utf8Sequences {
		if lead >= row.from && lead <= row.to {
			return row
		}
	}
	return wellFormed{}
}

// Stem is Python's PurePath(name).stem for a file name: the name less its
// last suffix. A suffix needs a dot that is neither the first nor the last
// character, so ".bashrc" and "notes." are their own stems.
func Stem(name string) string {
	if i := suffixAt(name); i >= 0 {
		return name[:i]
	}
	return name
}

// Suffix is Python's PurePath(name).suffix: the last suffix including its
// dot, or "" when Stem is the whole name.
func Suffix(name string) string {
	if i := suffixAt(name); i >= 0 {
		return name[i:]
	}
	return ""
}

func suffixAt(name string) int {
	i := strings.LastIndexByte(name, '.')
	if i > 0 && i < len(name)-1 {
		return i
	}
	return -1
}
