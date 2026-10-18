package domain

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// Verdict is how usable a document's chunks are.
//
// Separate from the integrity checks a repository makes: every defect here is
// compatible with a clean ingest that reached 'ready'. A document can be
// perfectly consistent and still be shaped so retrieval cannot work on it.
type Verdict uint8

const (
	VerdictGood Verdict = iota + 1
	VerdictDegraded
	VerdictUnusable
)

var verdictText = newEnumText("verdict", map[Verdict]string{
	VerdictGood:     "good",
	VerdictDegraded: "degraded",
	VerdictUnusable: "unusable",
})

func (v Verdict) String() string                { return verdictText.name(v) }
func (v Verdict) MarshalText() ([]byte, error)  { return verdictText.marshal(v) }
func (v *Verdict) UnmarshalText(b []byte) error { return verdictText.unmarshal(b, v) }

// Bounds the grader measures against.
const (
	// GradeMinChunks is the size below which rate-based checks are noise.
	// Grading a distribution needs a distribution: one short chunk in a
	// four-chunk document is not fragmentation.
	GradeMinChunks = 25

	// The size thresholds are expressed against the chunker's own bounds, so
	// the two notions of a well-sized chunk cannot drift apart. A chunk over
	// MaxTokens survived a subdivision pass, meaning it offered no internal
	// boundary to split on.
	OversizedDegradedRate = 0.10
	OversizedUnusableRate = 0.35

	// Merging forward already absorbs short unnumbered units, so what is
	// left under MinTokens is what merging could not fix.
	FragmentedDegradedRate = 0.30
	FragmentedUnusableRate = 0.60

	// FlatMinChunks is where a missing hierarchy starts to cost something.
	// Orientation is the largest measured retrieval lever and a document
	// with no hierarchy has none to offer, which costs nothing until there
	// are enough chunks for a cold search to get lost in.
	FlatMinChunks = 50

	// Distinct heading paths per chunk. Where a source missed headings,
	// boundaries came from the token budget rather than from the document,
	// consecutive slices inherit one heading and a section filter can no
	// longer address them apart. Well-structured corpora measure 0.84 and
	// 0.93; a manual whose structure was never derived, and which was
	// therefore cut into fixed windows, measures 0.29. Only a source that
	// can miss headings is graded on the degraded bound: see
	// StructureReport.Unaddressable. The unusable bound sets the severity of
	// either way a structure fails to separate its chunks.
	AddressableDegradedRatio = 0.50
	AddressableUnusableRatio = 0.35

	// FigureMaxTokens is the size under which a chunk carrying an image
	// holds its content in the picture, where no retrieval reaches it.
	FigureMaxTokens    = 40
	FigureDegradedRate = 0.25

	// The boilerplate bounds mirror the PDF adapter's own rule: a line
	// repeated across this share of a document is furniture. Running headers
	// and footers are short, so a long repeated passage is duplicated
	// content, a different defect.
	BoilerplateChunkFraction = 0.25
	BoilerplateMinChunks     = 8
	BoilerplateMaxLineChars  = 120
	// BoilerplateMinLineChars is what separates furniture from content.
	// Measured across four corpora, genuine unstripped furniture ran 66 to
	// 88 characters, while every repeated line below this bound was content:
	// stray single glyphs, a "NOTE" callout label, a recurring control name.
	BoilerplateMinLineChars = 20
)

// digitRun and spaceRun normalize a line so one footer counts once rather
// than once per page.
var (
	digitRun = regexp.MustCompile(`\d+`)
	spaceRun = regexp.MustCompile(`\s+`)
	// hasLetter is Python's `[^\W\d_]`, a letter in any script.
	hasLetter = regexp.MustCompile(`[\p{L}\p{Nl}\p{No}]`)
)

// Finding is one graded defect, its evidence, and what it costs a caller.
type Finding struct {
	Code     string  `json:"code"`
	Detail   string  `json:"detail"`
	Severity Verdict `json:"severity"`
}

// ChunkFacts are the per-chunk facts grading reads.
//
// Decoupled from the row shape, so the grader can be exercised on a
// constructed distribution rather than on a document that happens to have
// one.
type ChunkFacts struct {
	Text        string
	HeadingPath string
	Tokens      int
	ImageCount  int
	Depth       int
	// Numbered is true where the format gave the chunk an authoritative
	// section, which is what keeps it out of the mergeable population.
	Numbered bool
}

// FigureDominated reports a chunk whose content is in a picture.
func (c ChunkFacts) FigureDominated() bool {
	return c.ImageCount > 0 && c.Tokens < FigureMaxTokens
}

// FactsOf reads the facts the grader needs from a chunk.
func FactsOf(c Chunk) ChunkFacts {
	return ChunkFacts{
		Text:        c.Text,
		HeadingPath: c.HeadingPath,
		Tokens:      EstimateTokens(c.Text),
		ImageCount:  c.ImageCount,
		Depth:       len(strings.Split(c.HeadingPath, PathSep)),
		Numbered:    c.Section != nil && *c.Section != "",
	}
}

// Grade assesses whether chunking produced a searchable document. The
// source is what its headings were derived from, which decides whether few
// heading paths per chunk can mean missed headings.
func Grade(chunks []ChunkFacts, source StructureSource) []Finding {
	if len(chunks) < GradeMinChunks {
		return nil
	}
	addressable := func(chunks []ChunkFacts) *Finding { return unaddressable(chunks, source) }
	var out []Finding
	for _, check := range []func([]ChunkFacts) *Finding{
		oversized, fragmented, flatHierarchy, figureDominated, addressable, boilerplate,
	} {
		if finding := check(chunks); finding != nil {
			out = append(out, *finding)
		}
	}
	return out
}

// Verdict is the worst severity among the findings.
func GradeVerdict(findings []Finding) Verdict {
	worst := VerdictGood
	for _, f := range findings {
		if f.Severity > worst {
			worst = f.Severity
		}
	}
	return worst
}

// oversized reports chunks subdivision found no boundary inside.
func oversized(chunks []ChunkFacts) *Finding {
	var count, biggest int
	for _, c := range chunks {
		if c.Tokens > MaxTokens {
			count++
			biggest = max(biggest, c.Tokens)
		}
	}
	rate := float64(count) / float64(len(chunks))
	if count == 0 || rate < OversizedDegradedRate {
		return nil
	}
	return &Finding{
		Code:     "oversized",
		Severity: severity(rate >= OversizedUnusableRate),
		Detail: fmt.Sprintf(
			"%d of %d chunks (%s) exceed the %d-token chunk cap, the largest at %s. "+
				"Subdivision found no boundary inside them, which means the heading "+
				"structure was too coarse or was not derived at all. Search returns "+
				"whole chapters.",
			count, len(chunks), percent(rate), MaxTokens, thousands(biggest)),
	}
}

// fragmented reports prose a heading level split too finely.
//
// The mergeable population has to be large enough to carry a rate of its
// own: a document that numbers nearly everything leaves a handful of
// mergeable chunks, and one short chunk out of one is not a distribution.
func fragmented(chunks []ChunkFacts) *Finding {
	mergeable, fragments := mergeablePopulation(chunks)
	if mergeable < GradeMinChunks || fragments == 0 {
		return nil
	}
	rate := float64(fragments) / float64(mergeable)
	if rate < FragmentedDegradedRate {
		return nil
	}
	return &Finding{
		Code:     "fragmented",
		Severity: severity(rate >= FragmentedUnusableRate),
		Detail: fmt.Sprintf(
			"%d of %d mergeable chunks (%s) are under %d tokens after merge-forward. "+
				"A heading level was detected too eagerly and split prose into fragments, "+
				"so no single chunk carries enough context to answer a question.",
			fragments, mergeable, percent(rate), MinTokens),
	}
}

// mergeablePopulation counts the chunks merge-forward could have absorbed,
// and how many of them are still short.
func mergeablePopulation(chunks []ChunkFacts) (mergeable, fragments int) {
	for _, c := range chunks {
		if c.Numbered || c.FigureDominated() {
			continue
		}
		mergeable++
		if c.Tokens < MinTokens {
			fragments++
		}
	}
	return mergeable, fragments
}

// flatHierarchy reports a document with nothing to orient a caller by.
func flatHierarchy(chunks []ChunkFacts) *Finding {
	if len(chunks) < FlatMinChunks {
		return nil
	}
	deepest := 0
	for _, c := range chunks {
		deepest = max(deepest, c.Depth)
	}
	if deepest > 1 {
		return nil
	}
	return &Finding{
		Code:     "flat_hierarchy",
		Severity: VerdictDegraded,
		Detail: fmt.Sprintf(
			"All %d chunks sit at heading depth 1, so the document has no hierarchy. "+
				"`outline` cannot orient a caller and `section_filter` cannot narrow a "+
				"search -- the largest measured retrieval lever is unavailable and every "+
				"query falls back to cold keyword search.",
			len(chunks)),
	}
}

// figureDominated reports chunks whose content is in their pictures.
func figureDominated(chunks []ChunkFacts) *Finding {
	count := 0
	for _, c := range chunks {
		if c.FigureDominated() {
			count++
		}
	}
	rate := float64(count) / float64(len(chunks))
	if count == 0 || rate < FigureDegradedRate {
		return nil
	}
	return &Finding{
		Code:     "figure_dominated",
		Severity: VerdictDegraded,
		Detail: fmt.Sprintf(
			"%d of %d chunks (%s) carry an image and under %d tokens of text. Their "+
				"content is in the picture, which no retrieval reaches; a caller receives "+
				"the caption and must be told to look at the page.",
			count, len(chunks), percent(rate), FigureMaxTokens),
	}
}

// unaddressable reports chunks a section filter cannot tell apart: most of
// the document under one heading path, or, where the source could have
// missed headings, too few paths for the chunks.
func unaddressable(chunks []ChunkFacts, source StructureSource) *Finding {
	sections := sectionsOf(chunks)
	ratio := float64(sections.distinct) / float64(len(chunks))
	share := float64(sections.largest) / float64(len(chunks))
	undivided := share >= UndividedShare
	if !undivided && (source.MarkedUp() || ratio >= AddressableDegradedRatio) {
		return nil
	}
	finding := &Finding{
		Code:     "unaddressable",
		Severity: severity(ratio < AddressableUnusableRatio),
	}
	headless := ""
	if sections.headless > 0 {
		headless = fmt.Sprintf(
			" %d chunks carry no heading at all and cannot be reached by heading, "+
				"filtered, or described in an outline.", sections.headless)
	}
	switch {
	case undivided && pystr.Strip(sections.largestPath) == "":
		finding.Detail = fmt.Sprintf(
			"%d of %d chunks (%s) carry no heading at all, so their boundaries came "+
				"from the token budget rather than from the document. They cannot be "+
				"reached by heading or filtered, and `outline` cannot describe them.",
			sections.largest, len(chunks), percent(share))
	case undivided:
		finding.Detail = fmt.Sprintf(
			"%d of %d chunks (%s) share one heading path, %s, so their boundaries came "+
				"from the token budget rather than from the document. `section_filter` "+
				"cannot narrow within that part of it and `outline` gives that part one "+
				"entry.%s",
			sections.largest, len(chunks), percent(share),
			pystr.Repr(truncate(sections.largestPath, pathShown)), headless)
	default:
		finding.Detail = fmt.Sprintf(
			"%d chunks share only %d distinct heading paths (%.2f per chunk), under %s, "+
				"which can miss headings. Where it missed one, consecutive chunks inherit "+
				"the heading before it and their boundaries came from the token budget "+
				"rather than from the document. `section_filter` cannot separate them and "+
				"`outline` describes the document in %d entries.%s",
			len(chunks), sections.distinct, ratio, headingOrigin(source, "`%s`"),
			sections.distinct, headless)
	}
	return finding
}

// sectionCounts is how a document's chunks fall under its heading paths.
type sectionCounts struct {
	// largestPath holds the most chunks, the first in document order where
	// two hold as many.
	largestPath string
	distinct    int
	largest     int
	headless    int
}

// sectionsOf counts a document's chunks by heading path.
func sectionsOf(chunks []ChunkFacts) sectionCounts {
	perPath := map[string]int{}
	var out sectionCounts
	for _, c := range chunks {
		perPath[c.HeadingPath]++
		if pystr.Strip(c.HeadingPath) == "" {
			out.headless++
		}
	}
	out.distinct = len(perPath)
	// Counted before it is chosen: the first path to reach a count is not
	// the first in the document when a section's chunks are scattered.
	for _, c := range chunks {
		if perPath[c.HeadingPath] > out.largest {
			out.largestPath, out.largest = c.HeadingPath, perPath[c.HeadingPath]
		}
	}
	return out
}

// boilerplate reports page furniture the adapter did not strip.
func boilerplate(chunks []ChunkFacts) *Finding {
	repeated := repeatedLines(chunks)
	if len(repeated) == 0 {
		return nil
	}
	worst := repeated[0]
	return &Finding{
		Code:     "boilerplate",
		Severity: VerdictDegraded,
		Detail: fmt.Sprintf(
			"%d line(s) repeat across a quarter or more of the document, the most "+
				"frequent in %d of %d chunks: %s. Running headers and footers were not "+
				"stripped, so a share of the BM25 term mass is furniture and every chunk "+
				"matches it equally.",
			len(repeated), worst.count, len(chunks), pystr.Repr(truncate(worst.example, 80))),
	}
}

// repeat is one repeated line, counted by the chunks it appears in.
type repeat struct {
	example string
	count   int
}

// repeatedLines finds the short lines occurring in many chunks, with a
// verbatim example of each.
//
// Digits are normalized so a page footer counts as one line rather than one
// distinct line per page.
func repeatedLines(chunks []ChunkFacts) []repeat {
	counts, example := countCandidates(chunks)
	floor := max(BoilerplateMinChunks, int(float64(len(chunks))*BoilerplateChunkFraction))

	var out []repeat
	for key, n := range counts {
		if n >= floor {
			out = append(out, repeat{example: example[key], count: n})
		}
	}
	// By frequency, then by text, so a document graded twice reads the same.
	slices.SortFunc(out, func(a, b repeat) int {
		if by := cmp.Compare(b.count, a.count); by != 0 {
			return by
		}
		return cmp.Compare(a.example, b.example)
	})
	return out
}

// countCandidates tallies each furniture candidate by the chunks it appears
// in, and remembers the first spelling of each.
//
// Counted once per chunk: a footer appearing twice in one chunk is still one
// chunk's worth of evidence.
func countCandidates(chunks []ChunkFacts) (counts map[string]int, example map[string]string) {
	counts = map[string]int{}
	example = map[string]string{}
	for _, c := range chunks {
		for key, raw := range candidateLines(c.Text) {
			counts[key]++
			if _, seen := example[key]; !seen {
				example[key] = raw
			}
		}
	}
	return counts, example
}

// candidateLines maps each of a chunk's furniture candidates to the text it
// was written as.
func candidateLines(text string) map[string]string {
	lines := map[string]string{}
	for _, raw := range pystr.SplitLines(text) {
		line := pystr.Strip(raw)
		if !boilerplateCandidate(line) {
			continue
		}
		lines[digitRun.ReplaceAllString(spaceRun.ReplaceAllString(line, " "), "#")] = line
	}
	return lines
}

// boilerplateCandidate reports whether a line could be page furniture.
//
// A line with no letters is excluded, because digits are normalized: every
// bare number in the document collapses to one key, and numbered procedure
// steps are lines like "1" and "2" repeated throughout a manual. Pooling them
// would report the document's own instructions as furniture.
//
// A page number standing alone is furniture and is missed by the rule, as is
// a short recurring word. That is the right trade: both carry almost no BM25
// term mass, while what they would be confused with, procedure step numbers
// and callout labels and control names, carries the answer to every "how do
// I" query in the document.
func boilerplateCandidate(line string) bool {
	n := len([]rune(line))
	return n >= BoilerplateMinLineChars && n <= BoilerplateMaxLineChars &&
		hasLetter.MatchString(line)
}

func severity(unusable bool) Verdict {
	if unusable {
		return VerdictUnusable
	}
	return VerdictDegraded
}

// thousands renders an integer with Python's "{:,}" separators.
func thousands(n int) string {
	digits := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// truncate cuts a string to at most n runes, as Python's slice does.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
