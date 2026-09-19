package domain

// Offline token estimation.
//
// Deliberately dependency-free: ingest must never make a network call, which
// rules out downloading a tokenizer's vocabulary. Every threshold in the
// chunker is approximate ("~1,200 tokens"), so a calibrated estimate is
// sufficient. It counts word and punctuation atoms and scales by a subword
// factor, which tracks BPE within about 10% on Latin-script technical prose.
//
// Error direction matters more than error size. Under-counting is silent and
// compounding: a chunk whose tokens are under-counted never trips the
// subdivision cap, so it lands oversized and every size-based check downstream
// reads a figure in the wrong unit. Where this file guesses, it guesses high.

import (
	"math"
	"unicode"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// SubwordFactor is the average number of BPE sub-tokens per whitespace or
// punctuation atom in English technical prose. Raising it makes the chunker
// more conservative.
const SubwordFactor = 1.3

// cjkAtomWeight makes one CJK character about one token, expressed relative
// to SubwordFactor so CountAtoms keeps returning a value TokensFromAtoms can
// scale.
const cjkAtomWeight = 1.0 / SubwordFactor

// _cjk covers scripts written without spaces, where a character is roughly
// one BPE token on its own. Word-atom counting reads a whole run of these as
// one atom, which under-counts by roughly 9x on Japanese and 4x on Chinese.
var _cjk = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x3000, Hi: 0x303f, Stride: 1}, // CJK punctuation
		{Lo: 0x3040, Hi: 0x30ff, Stride: 1}, // hiragana and katakana
		{Lo: 0x3400, Hi: 0x4dbf, Stride: 1}, // Han, extension A
		{Lo: 0x4e00, Hi: 0x9fff, Stride: 1}, // Han
		{Lo: 0xac00, Hi: 0xd7af, Stride: 1}, // Hangul syllables
		{Lo: 0xf900, Hi: 0xfaff, Stride: 1}, // Han, compatibility ideographs
	},
}

// _uncalibrated covers scripts this estimator has no calibration for.
// Cyrillic and Greek split more aggressively than Latin, and the rest are
// unmeasured. Their presence is reported rather than guessed at.
var _uncalibrated = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x0370, Hi: 0x03ff, Stride: 1}, // Greek
		{Lo: 0x0400, Hi: 0x04ff, Stride: 1}, // Cyrillic
		{Lo: 0x0530, Hi: 0x058f, Stride: 1}, // Armenian
		{Lo: 0x0590, Hi: 0x05ff, Stride: 1}, // Hebrew
		{Lo: 0x0600, Hi: 0x06ff, Stride: 1}, // Arabic
		{Lo: 0x0900, Hi: 0x097f, Stride: 1}, // Devanagari
		{Lo: 0x0e00, Hi: 0x0e7f, Stride: 1}, // Thai
		{Lo: 0x10a0, Hi: 0x10ff, Stride: 1}, // Georgian
	},
}

// CountAtoms returns the raw word and punctuation atoms in text, before the
// subword factor.
//
// Accumulate these, never summed EstimateTokens results, when measuring a
// sequence of pieces against a budget. EstimateTokens truncates, so summing
// it over hundreds of short pieces compounds the loss into a budget check that
// silently never trips.
//
// Python counts `\w+|[^\w\s]` matches after replacing CJK characters with
// spaces, then adds the CJK count scaled and rounded half to even. One scan
// does the same: a CJK character ends a word run and counts separately.
func CountAtoms(text string) int {
	atoms, cjk := 0, 0
	inWord := false
	for _, r := range text {
		switch {
		case unicode.Is(_cjk, r):
			cjk++
			inWord = false
		case pystr.IsWord(r):
			if !inWord {
				atoms++
				inWord = true
			}
		case pystr.IsSpace(r):
			inWord = false
		default:
			atoms++
			inWord = false
		}
	}
	if cjk == 0 {
		return atoms
	}
	return atoms + int(math.RoundToEven(float64(cjk)*cjkAtomWeight))
}

// TokensFromAtoms converts an accumulated atom count to an estimated token
// count, truncating toward zero as Python's int() does.
func TokensFromAtoms(atoms int) int {
	return int(float64(atoms) * SubwordFactor)
}

// EstimateTokens approximates the BPE token count of text.
func EstimateTokens(text string) int {
	return TokensFromAtoms(CountAtoms(text))
}

// UncalibratedLetterShare is the share of letters written in a script this
// estimator cannot size. A document made mostly of such text has chunk sizes,
// and every size threshold applied to it, in an unknown unit.
//
// As in Python, the numerator counts every character in those script blocks,
// letter or not, and the denominator counts letters.
func UncalibratedLetterShare(text string) float64 {
	letters, uncal := 0, 0
	for _, r := range text {
		if pystr.IsLetter(r) {
			letters++
		}
		if unicode.Is(_uncalibrated, r) {
			uncal++
		}
	}
	if letters == 0 {
		return 0
	}
	return float64(uncal) / float64(letters)
}
