package domain

// Format-agnostic chunking.
//
// The chunk unit is the deepest numbered section a document declares. When a
// format supplies no numbering (Markdown, HTML, DOCX, plain text) the unit
// falls back to the deepest heading path. The chunker reads only the
// normalized Extraction; it never learns which adapter produced the blocks.
//
// Two rules do the shaping. A unit over MaxTokens subdivides, first at
// unnumbered subheadings and then at block boundaries. A unit under MinTokens
// merges forward into its next sibling, but never across a boundary the
// document declared with a section number: small sections stay small.
//
// Ported from python/docsearch/chunker.py, which explains each rule with the
// measurement behind it.

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// Chunk size bounds, in estimated tokens.
const (
	MaxTokens = 1200
	MinTokens = 100
	// StubMaxTokens is the size under which a parent section whose next unit
	// is its own child counts as a chapter stub, a title plus a sentence of
	// preamble, and folds into that child. BM25's length normalization would
	// otherwise let an 18-token chapter title outrank the subsection that
	// answers the query.
	StubMaxTokens = 30
)

// PathSep joins a heading path for storage and display.
const PathSep = " > "

// A family of at least ReferenceFamilyMin chunks under a parent heading that
// declares itself a listing, whose leaves are mostly short names, is a
// reference index rather than a topic.
const (
	ReferenceFamilyMin    = 30
	referenceLeafMaxWords = 4
	referenceLeafRate     = 0.6
)

// _referenceParent matches a heading that declares its children a listing of
// entries. Size alone cannot tell a reference index from a chapter of uniform
// siblings, so the document's own declaration is the whole signal. Python's
// `\s` and `\b` are Unicode-aware; `[`+SpaceChars+`]` stands in for the first
// and wordBounded checks the second.
var _referenceParent = regexp.MustCompile(`(?i)keywords?|glossary` +
	`|error[` + pystr.SpaceChars + `]+(?:codes?|messages?)` +
	`|(?:command|api|syntax|function)[` + pystr.SpaceChars + `]+(?:reference|index|listing)`)

// Chunks cuts an extraction into retrievable chunks and marks keyword
// reference families.
func Chunks(ext Extraction) []Chunk {
	var chunks []Chunk
	for _, u := range foldChapterStubs(mergeSmall(group(ext.Blocks))) {
		for _, p := range unitParts(u) {
			chunks = append(chunks, emit(len(chunks), p.path, p.blocks, u.section))
		}
	}
	ClassifyKinds(chunks)
	return chunks
}

// ClassifyKinds marks chunks belonging to a self-declared keyword reference
// index as KindKeywordReference. Such a family is term-dense and low-prose, so
// its entries surface for any query sharing a token with a keyword name. They
// are marked, never dropped: a keyword lookup is a legitimate query and these
// are its answers.
//
// Families are keyed on the shallowest ancestor declared as a listing, so an
// entry that subdivision split one level deeper stays with its siblings.
func ClassifyKinds(chunks []Chunk) {
	families := map[string][]int{}
	for i := range chunks {
		if key, ok := referenceFamily(chunks[i].HeadingPath); ok {
			families[key] = append(families[key], i)
		}
	}
	for _, members := range families {
		if !isReferenceListing(chunks, members) {
			continue
		}
		for _, i := range members {
			chunks[i].Kind = KindKeywordReference
		}
	}
}

// unit is a run of consecutive blocks sharing one section and heading path.
type unit struct {
	section     *string
	headingPath []string
	blocks      []Block
}

// text is the unit's blocks joined and stripped.
func (u unit) text() string {
	return pystr.Strip(joinTexts(u.blocks))
}

// tokens is the estimated size of the unit's text.
func (u unit) tokens() int {
	return EstimateTokens(u.text())
}

// part is a slice of an oversized unit, with the heading path it is filed
// under.
type part struct {
	path   []string
	blocks []Block
}

// run is a subheading and the blocks under it until the next one.
type run struct {
	label  string
	blocks []Block
}

// group gathers consecutive blocks sharing both a section and a heading path.
// Keying on the section alone would keep only the first block's path, which
// collapses a documentation site: its section is the whole page.
func group(blocks []Block) []unit {
	var units []unit
	for _, b := range blocks {
		if n := len(units); n > 0 && sameSection(units[n-1].section, b.Section) &&
			slices.Equal(units[n-1].headingPath, b.HeadingPath) {
			units[n-1].blocks = append(units[n-1].blocks, b)
			continue
		}
		units = append(units, unit{
			section:     b.Section,
			headingPath: slices.Clone(b.HeadingPath),
			blocks:      []Block{b},
		})
	}
	return units
}

// mergeSmall merges each unit under MinTokens forward into the units after it,
// while they share its section and parent heading. A heading inside one
// section is the document's, but not a boundary it declared.
func mergeSmall(units []unit) []unit {
	var out []unit
	for i := 0; i < len(units); {
		merged, next := absorbFollowing(units, i)
		out = append(out, merged)
		i = next
	}
	return out
}

// absorbFollowing grows units[i] with the units after it while it stays under
// MinTokens and each next unit is mergeable, returning the grown unit and the
// index of the first unit it did not absorb.
func absorbFollowing(units []unit, i int) (unit, int) {
	cur := units[i]
	j := i + 1
	for j < len(units) && cur.tokens() < MinTokens && mergeable(cur, units[j]) {
		cur = unit{
			section:     cur.section,
			headingPath: parentOrSelf(cur.headingPath),
			blocks:      concat(cur.blocks, units[j].blocks),
		}
		j++
	}
	return cur, j
}

// mergeable reports whether next may merge into cur: same section, and the
// same parent heading.
func mergeable(cur, next unit) bool {
	return sameSection(next.section, cur.section) &&
		slices.Equal(parent(cur.headingPath), parent(next.headingPath))
}

// foldChapterStubs folds a parent section's thin preamble into its first
// child, so a chapter title does not compete with the subsection below it.
func foldChapterStubs(units []unit) []unit {
	units = slices.Clone(units)
	var out []unit
	for i, cur := range units {
		if i+1 < len(units) && isStub(cur, units[i+1]) {
			next := units[i+1]
			units[i+1] = unit{
				section:     next.section,
				headingPath: next.headingPath,
				blocks:      concat(cur.blocks, next.blocks),
			}
			continue
		}
		out = append(out, cur)
	}
	return out
}

// isStub reports whether cur is a numbered section too small to stand alone,
// followed by one of its own subsections.
func isStub(cur, next unit) bool {
	return cur.section != nil && isDescendant(next.section, cur.section) &&
		cur.tokens() < StubMaxTokens
}

// unitParts is what a unit contributes as chunks: nothing when it holds no
// text, itself when it fits under MaxTokens, and otherwise its subdivisions
// that hold any text.
func unitParts(u unit) []part {
	text := u.text()
	if text == "" {
		return nil
	}
	if EstimateTokens(text) <= MaxTokens {
		return []part{{path: u.headingPath, blocks: u.blocks}}
	}
	var parts []part
	for _, p := range capAtBlocks(subdivideAtHeadings(u)) {
		if anyText(p.blocks) {
			parts = append(parts, p)
		}
	}
	return parts
}

// subdivideAtHeadings splits an oversized unit at the unnumbered subheadings
// the adapter marked, packing consecutive runs up to the cap. Splitting once
// per subheading would shatter a 1,300-token section into eight 160-token
// fragments.
func subdivideAtHeadings(u unit) []part {
	runs := subheadingRuns(u.blocks)
	if len(runs) <= 1 {
		return []part{{path: u.headingPath, blocks: u.blocks}}
	}
	var parts []part
	for _, packed := range packByAtoms(runs, runAtoms) {
		parts = append(parts, part{
			path:   packedPath(u.headingPath, packed),
			blocks: runBlocks(packed),
		})
	}
	return parts
}

// capAtBlocks splits any part still over MaxTokens greedily at block
// boundaries, after splitting any single oversized block at its paragraphs.
func capAtBlocks(parts []part) []part {
	var out []part
	for _, p := range parts {
		if EstimateTokens(joinTexts(p.blocks)) <= MaxTokens {
			out = append(out, p)
			continue
		}
		var expanded []Block
		for _, b := range p.blocks {
			expanded = append(expanded, splitBlock(b)...)
		}
		for _, blocks := range packByAtoms(expanded, blockAtoms) {
			out = append(out, part{path: p.path, blocks: blocks})
		}
	}
	return out
}

// subheadingRuns splits blocks into runs, each starting at a subheading the
// adapter marked. Blocks before the first subheading form an unlabelled run.
func subheadingRuns(blocks []Block) []run {
	var runs []run
	for _, b := range blocks {
		switch {
		case b.Subdivision && !b.FigureOnly:
			runs = append(runs, run{label: firstLine(b.Text), blocks: []Block{b}})
		case len(runs) > 0:
			runs[len(runs)-1].blocks = append(runs[len(runs)-1].blocks, b)
		default:
			runs = append(runs, run{blocks: []Block{b}})
		}
	}
	return runs
}

// packedPath files a packed group under its subheading only when it holds
// exactly one named subheading; a group spanning several must not
// misattribute itself.
func packedPath(headingPath []string, runs []run) []string {
	path := slices.Clone(headingPath)
	var named []string
	for _, r := range runs {
		if r.label != "" {
			named = append(named, r.label)
		}
	}
	if len(named) == 1 {
		path = append(path, named[0])
	}
	return path
}

// splitBlock splits one oversized block at paragraph, then line, boundaries.
// Without it a section holding a single enormous block, such as a dense table
// or a back-of-book index, has no interior boundary to cut on.
func splitBlock(b Block) []Block {
	if EstimateTokens(b.Text) <= MaxTokens {
		return []Block{b}
	}
	pieces := strings.Split(b.Text, "\n\n")
	if len(pieces) == 1 {
		pieces = pystr.SplitLines(b.Text)
	}
	var out []Block
	for _, group := range packByAtoms(pieces, CountAtoms) {
		piece := b
		piece.Text = strings.Join(group, "\n")
		out = append(out, piece)
	}
	return out
}

// packByAtoms groups items greedily, starting a new group when adding the
// next item would take the group over MaxTokens. Sizes are accumulated in
// atoms and converted once, so truncation cannot compound across many small
// items into a cap that never trips.
func packByAtoms[T any](items []T, atoms func(T) int) [][]T {
	var groups [][]T
	var cur []T
	total := 0
	for _, item := range items {
		n := atoms(item)
		if len(cur) > 0 && TokensFromAtoms(total+n) > MaxTokens {
			groups = append(groups, cur)
			cur, total = nil, 0
		}
		cur = append(cur, item)
		total += n
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

// emit builds a chunk from blocks, taking page range, printed page and images
// from all of them and the address from the block it starts at, which is
// where a citation should land.
func emit(ordinal int, path []string, blocks []Block, section *string) Chunk {
	start, end := pageRange(blocks)
	return Chunk{
		Ordinal:          ordinal,
		HeadingPath:      joinPath(path),
		Text:             pystr.Strip(joinTexts(blocks)),
		Section:          section,
		PageStart:        start,
		PageEnd:          end,
		PrintedPageStart: firstPrintedPage(blocks),
		ImageCount:       imageCount(blocks),
		Kind:             KindProse,
		URL:              blocks[0].URL,
		Fragment:         blocks[0].Fragment,
	}
}

// pageRange is the lowest page and highest page end across the blocks that
// carry a page locator; both nil when none does.
func pageRange(blocks []Block) (start, end *int) {
	for _, b := range blocks {
		page, ok := b.Locator["page"]
		if !ok {
			continue
		}
		last, ok := b.Locator["page_end"]
		if !ok {
			last = page
		}
		start, end = lower(start, page), higher(end, last)
	}
	return start, end
}

// firstPrintedPage is the lowest printed page number across the blocks.
func firstPrintedPage(blocks []Block) *int {
	var first *int
	for _, b := range blocks {
		if b.PrintedPage != nil {
			first = lower(first, *b.PrintedPage)
		}
	}
	return first
}

// referenceFamily returns the heading path of the shallowest ancestor that
// declares itself a listing, and whether there is one. The leaf itself is
// never its own family's parent.
func referenceFamily(headingPath string) (string, bool) {
	parts := strings.Split(headingPath, PathSep)
	for depth := 1; depth < len(parts); depth++ {
		if declaresListing(parts[depth-1]) {
			return strings.Join(parts[:depth], PathSep), true
		}
	}
	return "", false
}

// isReferenceListing reports whether a family is large enough, and its leaf
// headings named entries rather than described ones often enough, to be a
// reference index.
func isReferenceListing(chunks []Chunk, members []int) bool {
	if len(members) < ReferenceFamilyMin {
		return false
	}
	named := 0
	for _, i := range members {
		path := chunks[i].HeadingPath
		leaf := path[strings.LastIndex(path, PathSep)+len(PathSep):]
		if strings.LastIndex(path, PathSep) < 0 {
			leaf = path
		}
		if letterWords(leaf) <= referenceLeafMaxWords {
			named++
		}
	}
	return float64(named)/float64(len(members)) >= referenceLeafRate
}

// declaresListing reports whether a heading matches _referenceParent at Unicode
// word boundaries, as Python's `\b` requires.
func declaresListing(heading string) bool {
	for _, loc := range _referenceParent.FindAllStringIndex(heading, -1) {
		if wordBounded(heading, loc[0], loc[1]) {
			return true
		}
	}
	return false
}

// wordBounded reports whether s[start:end] begins and ends at a Unicode word
// boundary.
func wordBounded(s string, start, end int) bool {
	before, _ := utf8.DecodeLastRuneInString(s[:start])
	after, _ := utf8.DecodeRuneInString(s[end:])
	return (start == 0 || !pystr.IsWord(before)) && (end == len(s) || !pystr.IsWord(after))
}

// letterWords counts runs of letters, so a section number in a heading does
// not register as a word.
func letterWords(s string) int {
	words, inWord := 0, false
	for _, r := range s {
		letter := pystr.IsLetter(r)
		if letter && !inWord {
			words++
		}
		inWord = letter
	}
	return words
}

// firstLine is the first line of text, stripped: a subheading's label.
func firstLine(text string) string {
	lines := pystr.SplitLines(text)
	if len(lines) == 0 {
		return ""
	}
	return pystr.Strip(lines[0])
}

func joinTexts(blocks []Block) string {
	texts := make([]string, len(blocks))
	for i, b := range blocks {
		texts[i] = b.Text
	}
	return strings.Join(texts, "\n\n")
}

// joinPath joins the non-empty parts of a heading path.
func joinPath(path []string) string {
	var parts []string
	for _, p := range path {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, PathSep)
}

func anyText(blocks []Block) bool {
	return slices.ContainsFunc(blocks, func(b Block) bool { return pystr.Strip(b.Text) != "" })
}

func imageCount(blocks []Block) int {
	total := 0
	for _, b := range blocks {
		total += b.ImageCount
	}
	return total
}

func runAtoms(r run) int {
	total := 0
	for _, b := range r.blocks {
		total += CountAtoms(b.Text)
	}
	return total
}

func blockAtoms(b Block) int { return CountAtoms(b.Text) }

func runBlocks(runs []run) []Block {
	var blocks []Block
	for _, r := range runs {
		blocks = append(blocks, r.blocks...)
	}
	return blocks
}

// concat returns a new slice holding a then b, so neither input is aliased.
func concat(a, b []Block) []Block {
	return append(slices.Clone(a), b...)
}

// parent is a heading path without its last element.
func parent(path []string) []string {
	if len(path) == 0 {
		return nil
	}
	return path[:len(path)-1]
}

// parentOrSelf is the parent heading path, or the path itself when it has no
// parent: Python's `path[:-1] or path`.
func parentOrSelf(path []string) []string {
	if len(path) > 1 {
		return slices.Clone(path[:len(path)-1])
	}
	return slices.Clone(path)
}

func sameSection(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// isDescendant reports whether child is a numbered subsection of parent.
func isDescendant(child, parent *string) bool {
	if child == nil || parent == nil || *child == "" || *parent == "" {
		return false
	}
	return strings.HasPrefix(*child, *parent+".")
}

func lower(cur *int, v int) *int {
	if cur == nil || v < *cur {
		return &v
	}
	return cur
}

func higher(cur *int, v int) *int {
	if cur == nil || v > *cur {
		return &v
	}
	return cur
}
