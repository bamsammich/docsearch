package store

import "regexp"

// The Go mirror of docsearch.tokens. Kept deliberately identical so a span
// the server trims and a chunk the ingester sized are measured the same way.
//
// Callers accumulating over many pieces must sum atoms and convert once:
// converting per piece truncates each time, and over hundreds of short pieces
// the compounded rounding silently defeats a budget check.

var atomRe = regexp.MustCompile(`[\p{L}\p{N}_]+|[^\p{L}\p{N}\s]`)

const subwordFactor = 1.3

func countAtoms(text string) int { return len(atomRe.FindAllString(text, -1)) }

func tokensFromAtoms(atoms int) int { return int(float64(atoms) * subwordFactor) }

func estimateTokens(text string) int { return tokensFromAtoms(countAtoms(text)) }
