package pystr

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// ErrNotDigits is returned by Atoi for a string that is not all decimal
// digits.
var ErrNotDigits = errors.New("not a run of decimal digits")

// Round is Python's round(x, digits) on a float: the value written with that
// many decimals, rounding the exact binary value half to even, read back.
// math.Round(x*10)/10 differs where x*10 is inexact, as 0.15 is.
func Round(x float64, digits int) float64 {
	r, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', digits, 64), 64)
	if err != nil {
		// FormatFloat's output always parses; x is NaN or infinite here,
		// which Python's round returns unchanged too.
		return x
	}
	return r
}

// FloorDiv is Python's x // y on floats, computed as CPython's float_floor_div
// does: from the remainder, so the quotient of 403.99999999999994 by 4 is
// 100.0, where math.Floor(x/y) gives 101.
func FloorDiv(x, y float64) float64 {
	mod := math.Mod(x, y)
	div := (x - mod) / y
	if mod != 0 && (y < 0) != (mod < 0) {
		div--
	}
	if div == 0 {
		return math.Copysign(0, x/y)
	}
	floor := math.Floor(div)
	if div-floor > 0.5 {
		floor++
	}
	return floor
}

// Atoi is Python's int() of a string of decimal digits in any script, such as
// Arabic-Indic or fullwidth digits. It accepts exactly what the regexp class
// \p{Nd} matches.
func Atoi(s string) (int, error) {
	if s == "" {
		return 0, ErrNotDigits
	}
	n := 0
	for _, r := range s {
		d, ok := digitValue(r)
		if !ok {
			return 0, ErrNotDigits
		}
		n = n*10 + d
	}
	return n, nil
}

// IsDigits reports whether s is a non-empty run of decimal digits, which
// Atoi reads. Python's str.isdigit also accepts superscripts and other
// digits int() then refuses; where Python would raise, Go says no.
func IsDigits(s string) bool {
	_, err := Atoi(s)
	return err == nil
}

// digitValue is the value of a decimal digit. Unicode assigns every script's
// digits as contiguous runs from zero to nine, so the value is the distance
// from the start of the run the digit sits in, modulo ten where runs abut.
func digitValue(r rune) (int, bool) {
	if r >= '0' && r <= '9' {
		return int(r - '0'), true
	}
	if !unicode.IsDigit(r) {
		return 0, false
	}
	start := r
	for unicode.IsDigit(start - 1) {
		start--
	}
	return int(r-start) % 10, true
}

// The two runes whose full lowercase mapping differs from the simple one.
const (
	capitalIWithDot = rune(0x0130) // LATIN CAPITAL LETTER I WITH DOT ABOVE
	capitalSigma    = rune(0x03a3) // GREEK CAPITAL LETTER SIGMA
	finalSigma      = rune(0x03c2) // GREEK SMALL LETTER FINAL SIGMA
	dotAbove        = rune(0x0307) // COMBINING DOT ABOVE, after "i"
)

// Lower is Python's str.lower(), which applies Unicode's full lowercase
// mapping. It differs from strings.ToLower in two places: a capital I with a
// dot becomes "i" and a combining dot, and a capital sigma ending a word
// becomes the final form.
func Lower(s string) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range runes {
		switch {
		case r == capitalIWithDot:
			b.WriteByte('i')
			b.WriteRune(dotAbove)
		case r == capitalSigma && endsWord(runes, i):
			b.WriteRune(finalSigma)
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// endsWord is Unicode's Final_Sigma condition for the rune at i: a cased
// letter comes before it and none follows, skipping case-ignorable runes on
// both sides.
func endsWord(runes []rune, i int) bool {
	j := i - 1
	for j >= 0 && isCaseIgnorable(runes[j]) {
		j--
	}
	if j < 0 || !isCased(runes[j]) {
		return false
	}
	k := i + 1
	for k < len(runes) && isCaseIgnorable(runes[k]) {
		k++
	}
	return k == len(runes) || !isCased(runes[k])
}

func isCased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.In(r, unicode.Other_Lowercase, unicode.Other_Uppercase)
}

// wordMedial holds the punctuation Unicode's Case_Ignorable property takes
// from word-break classes MidLetter, MidNumLet and Single_Quote.
var wordMedial = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x0027, Hi: 0x0027, Stride: 1},
		{Lo: 0x002e, Hi: 0x002e, Stride: 1},
		{Lo: 0x003a, Hi: 0x003a, Stride: 1},
		{Lo: 0x00b7, Hi: 0x00b7, Stride: 1},
		{Lo: 0x0387, Hi: 0x0387, Stride: 1},
		{Lo: 0x055f, Hi: 0x055f, Stride: 1},
		{Lo: 0x05f4, Hi: 0x05f4, Stride: 1},
		{Lo: 0x2018, Hi: 0x2019, Stride: 1},
		{Lo: 0x2024, Hi: 0x2024, Stride: 1},
		{Lo: 0x2027, Hi: 0x2027, Stride: 1},
		{Lo: 0xfe13, Hi: 0xfe13, Stride: 1},
		{Lo: 0xfe52, Hi: 0xfe52, Stride: 1},
		{Lo: 0xfe55, Hi: 0xfe55, Stride: 1},
		{Lo: 0xff07, Hi: 0xff07, Stride: 1},
		{Lo: 0xff0e, Hi: 0xff0e, Stride: 1},
		{Lo: 0xff1a, Hi: 0xff1a, Stride: 1},
	},
	LatinOffset: 4,
}

func isCaseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk, wordMedial)
}
