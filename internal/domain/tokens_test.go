package domain_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

const (
	englishText  = "The console stores each cue in a sequence and plays it back on an executor. "
	japaneseText = "ミキシングコンソールはチャンネルごとに信号を処理します。"
	chineseText  = "调音台按通道处理信号。请操作推子来调整音量。"
	koreanText   = "믹싱 콘솔은 채널별로 신호를 처리합니다."
	russianText  = "Микшерная консоль обрабатывает сигнал по каждому каналу отдельно. "
)

// TokensSuite covers token estimation across scripts. No tokenizer is
// available offline by design, so these assert properties rather than exact
// BPE counts: that Latin calibration is unchanged, that scripts written
// without spaces are not under-counted, and that the accumulate-then-convert
// contract the chunker depends on still holds. Ported from
// tests/test_tokens.py.
type TokensSuite struct{ suite.Suite }

func TestTokens(t *testing.T) { suite.Run(t, new(TokensSuite)) }

// charsPerToken is Python's len(text) / estimate_tokens(text): len counts
// code points, not bytes.
func charsPerToken(text string) float64 {
	return float64(utf8.RuneCountInString(text)) / float64(domain.EstimateTokens(text))
}

// Latin technical prose runs about four characters per BPE token. Moving
// that silently re-sizes every chunk in every index.
func (s *TokensSuite) TestLatinCalibrationIsUnchanged() {
	ratio := charsPerToken(strings.Repeat(englishText, 20))
	s.GreaterOrEqual(ratio, 3.0)
	s.LessOrEqual(ratio, 4.5)
}

// A character in these scripts is roughly one token. Word-atom counting reads
// a whole run as one atom, and a low estimate is the dangerous direction.
func (s *TokensSuite) TestSpaceFreeScriptsAreNotUnderCounted() {
	tests := []struct {
		name string
		text string
	}{
		{name: "Japanese", text: japaneseText},
		{name: "Chinese", text: chineseText},
		{name: "Korean", text: koreanText},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.LessOrEqual(charsPerToken(strings.Repeat(tt.text, 20)), 1.6)
		})
	}
}

func (s *TokensSuite) TestACJKCharacterIsAboutOneToken() {
	han := strings.Repeat("調整音量信号処理", 40)
	perChar := float64(domain.EstimateTokens(han)) / float64(utf8.RuneCountInString(han))
	s.GreaterOrEqual(perChar, 0.8)
	s.LessOrEqual(perChar, 1.4)
}

func (s *TokensSuite) TestMixedScriptTextCountsBothHalves() {
	english, japanese := strings.Repeat(englishText, 5), strings.Repeat(japaneseText, 5)
	mixed := domain.EstimateTokens(english + japanese)
	s.Greater(mixed, domain.EstimateTokens(english))
	s.Greater(mixed, domain.EstimateTokens(japanese))
}

// Removing CJK before word-atom counting must not leave the run behind as one
// more atom. round(4 / 1.3) is 3 under Python's round-half-to-even.
func (s *TokensSuite) TestIdeographRunsAreNotDoubleCounted() {
	han := "信号処理"
	s.Equal(3, domain.CountAtoms(han))
	s.Equal(3, domain.CountAtoms(han+" "))
}

// The chunker sums atoms across blocks and converts once. Summing
// EstimateTokens instead compounds truncation into a budget check that
// silently never trips.
func (s *TokensSuite) TestAccumulateThenConvertMatchesDirectEstimation() {
	pieces := []string{englishText, japaneseText, chineseText, englishText, koreanText}
	atoms := 0
	for _, p := range pieces {
		atoms += domain.CountAtoms(p)
	}
	s.Equal(domain.EstimateTokens(strings.Join(pieces, "")), domain.TokensFromAtoms(atoms))
}

func (s *TokensSuite) TestUncalibratedLetterShare() {
	tests := []struct {
		name     string
		text     string
		min, max float64
	}{
		{name: "Russian is reported", text: strings.Repeat(russianText, 5), min: 0.9, max: 1},
		{name: "English is calibrated", text: strings.Repeat(englishText, 5), min: 0, max: 0},
		{name: "Japanese is calibrated", text: strings.Repeat(japaneseText, 5), min: 0, max: 0},
		{name: "empty text", text: "", min: 0, max: 0},
		{
			name: "half Russian is a partial share",
			text: strings.Repeat(englishText, 3) + strings.Repeat(russianText, 3),
			min:  0.2, max: 0.8,
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			share := domain.UncalibratedLetterShare(tt.text)
			s.GreaterOrEqual(share, tt.min)
			s.LessOrEqual(share, tt.max)
		})
	}
}
