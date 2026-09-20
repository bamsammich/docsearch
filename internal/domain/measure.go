package domain

import "slices"

// Measurements are what a document's chunks show about how it was stored.
//
// Separate from grading: these are integrity facts, and a defect among them
// means the rows disagree with each other or with the document they came
// from. A document can measure badly and grade well, or the reverse.
type Measurements struct {
	// UncoveredPages are the pages of a paginated document no chunk claims.
	UncoveredPages []int `json:"uncovered_pages"`
	// OrdinalGaps name where the numbering skips, by the ordinal before the
	// gap. A gap means a batch was lost between transactions.
	OrdinalGaps []int `json:"ordinal_gaps"`
	// ScatteredSections are sections whose chunks are not contiguous, which
	// happens when a boundary misfired and split one section key across the
	// document. It silently breaks the index_terms join.
	ScatteredSections []string `json:"scattered_sections"`
	// Longest and Shortest are the extremes, which is where a structural
	// extraction failure shows first.
	Longest  []SizedChunk `json:"longest"`
	Shortest []SizedChunk `json:"shortest"`
	Tokens   TokenSpread  `json:"tokens"`
	// MissingLocator counts chunks of a paginated document that name no
	// page, so a result cannot cite where it came from.
	MissingLocator   int `json:"missing_locator"`
	ChunksWithImages int `json:"chunks_with_images"`
	ChunkCount       int `json:"chunk_count"`
}

// TokenSpread is the distribution of chunk sizes.
//
// Eyeballing it catches a structural extraction failure immediately: a
// heading source that silently collapsed shows up as a handful of enormous
// chunks, and a shattered one as hundreds of near-empty chunks.
type TokenSpread struct {
	Min    int `json:"min"`
	Median int `json:"median"`
	P95    int `json:"p95"`
	Max    int `json:"max"`
	Mean   int `json:"mean"`
	Total  int `json:"total"`
}

// SizedChunk names one chunk by where it sits and how big it is.
type SizedChunk struct {
	HeadingPath string `json:"heading_path"`
	Ordinal     int    `json:"ordinal"`
	Tokens      int    `json:"tokens"`
}

// extremes is how many of the largest and smallest chunks a report names.
const extremes = 10

// Measure reads what a document's chunks show. pageCount is the document's
// own page count, and nil for a format without pages.
func Measure(chunks []Chunk, pageCount *int) Measurements {
	m := Measurements{
		ChunkCount:        len(chunks),
		ScatteredSections: orEmptyStrings(ScatteredSections(chunks)),
		UncoveredPages:    []int{},
		OrdinalGaps:       []int{},
		Longest:           []SizedChunk{},
		Shortest:          []SizedChunk{},
	}
	if len(chunks) == 0 {
		return m
	}

	sized := make([]SizedChunk, len(chunks))
	counts := make([]int, len(chunks))
	for i, c := range chunks {
		tokens := EstimateTokens(c.Text)
		sized[i] = SizedChunk{
			HeadingPath: c.HeadingPath, Ordinal: c.Ordinal, Tokens: tokens,
		}
		counts[i] = tokens
		if c.ImageCount > 0 {
			m.ChunksWithImages++
		}
		if pageCount != nil && c.PageStart == nil {
			m.MissingLocator++
		}
	}

	m.Tokens = spread(counts)
	m.Longest, m.Shortest = extremesOf(sized)
	m.UncoveredPages = uncoveredPages(chunks, pageCount)
	m.OrdinalGaps = ordinalGaps(chunks)
	return m
}

// spread is the token distribution, with the percentiles Python picks.
func spread(counts []int) TokenSpread {
	sorted := slices.Sorted(slices.Values(counts))
	total := 0
	for _, n := range sorted {
		total += n
	}
	return TokenSpread{
		Min:    sorted[0],
		Median: percentile(sorted, 0.5),
		P95:    percentile(sorted, 0.95),
		Max:    sorted[len(sorted)-1],
		Mean:   total / len(sorted),
		Total:  total,
	}
}

// percentile indexes a sorted slice the way Python's _pct does.
func percentile(sorted []int, q float64) int {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(len(sorted)-1, int(float64(len(sorted))*q))]
}

// extremesOf names the largest and smallest chunks, largest first.
func extremesOf(sized []SizedChunk) (longest, shortest []SizedChunk) {
	bySize := slices.Clone(sized)
	// Stable, so two chunks of one size keep document order and a report
	// read twice names the same chunks.
	slices.SortStableFunc(bySize, func(a, b SizedChunk) int { return a.Tokens - b.Tokens })

	shortest = slices.Clone(bySize[:min(len(bySize), extremes)])
	tail := bySize[max(0, len(bySize)-extremes):]
	longest = slices.Clone(tail)
	slices.Reverse(longest)
	return longest, shortest
}

// uncoveredPages are the pages no chunk claims, for a paginated document.
func uncoveredPages(chunks []Chunk, pageCount *int) []int {
	if pageCount == nil || *pageCount == 0 {
		return []int{}
	}
	covered := coveredPages(chunks)
	out := []int{}
	for page := 1; page <= *pageCount; page++ {
		if !covered[page] {
			out = append(out, page)
		}
	}
	return out
}

// coveredPages are the pages some chunk claims.
func coveredPages(chunks []Chunk) map[int]bool {
	covered := map[int]bool{}
	for _, c := range chunks {
		if c.PageStart == nil {
			continue
		}
		last := *c.PageStart
		if c.PageEnd != nil {
			last = *c.PageEnd
		}
		for page := *c.PageStart; page <= last; page++ {
			covered[page] = true
		}
	}
	return covered
}

// ordinalGaps name where the numbering skips, by the ordinal before each gap.
func ordinalGaps(chunks []Chunk) []int {
	out := []int{}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Ordinal != chunks[i-1].Ordinal+1 {
			out = append(out, chunks[i-1].Ordinal+1)
		}
	}
	return out
}

func orEmptyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
