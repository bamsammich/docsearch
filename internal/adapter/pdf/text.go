package pdf

import (
	"regexp"
	"slices"
	"strings"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// Patterns from the Python adapter. Python's `\d` and `\s` are Unicode
// classes and Go's are ASCII, so both are spelled out: `\p{Nd}` is exactly
// Python's `\d` on str patterns, and pystr.SpaceChars its `\s`.
const (
	space = `[` + pystr.SpaceChars + `]`
	num   = `\p{Nd}+(?:\.\p{Nd}+)*`
)

var (
	digitRun      = regexp.MustCompile(`\p{Nd}+`)
	pageOf        = regexp.MustCompile(`^\p{Nd}+` + space + `+of` + space + `+\p{Nd}+$`)
	sectionLine   = regexp.MustCompile(`^(` + num + `)\.` + space + `*(.*)$`)
	sectionOnly   = regexp.MustCompile(`^(` + num + `)\.$`)
	sectionMerged = regexp.MustCompile(`^(` + num + `)\.` + space + `+(.+)$`)
	indexEntry    = regexp.MustCompile(`^(.*?)` + space + `+((?:` + num + `\.` + space + `*)+)$`)
	sectionRef    = regexp.MustCompile(num)
	nonWord       = regexp.MustCompile(`[^` + pystr.WordChars + `]+`)
	// contentsLeader is "Introduction .......... 11": leader dots, bullets or
	// dashes, then a page number.
	contentsLeader = regexp.MustCompile(
		`[` + pystr.SpaceChars + `.\x{b7}\x{2022}\-\x{2014}_]*\p{Nd}*` + space + `*$`)
)

// normalize collapses every run of digits to "#", so a running footer reads
// the same on every page whatever its page number.
func normalize(text string) string {
	return digitRun.ReplaceAllString(text, "#")
}

// boilerplate is the set of normalized lines found to repeat as page
// furniture.
type boilerplate map[string]bool

func (b boilerplate) matches(text string) bool {
	return b[normalize(text)] || pageOf.MatchString(text)
}

// normalizeTitle lowercases a title and reduces its punctuation to single
// spaces, so a contents row and the heading it names compare equal.
func normalizeTitle(text string) string {
	return pystr.Strip(nonWord.ReplaceAllString(pystr.Lower(text), " "))
}

// sectionKey is a section number as integers, "7.17" as [7 17].
func sectionKey(section string) []int {
	parts := strings.Split(section, ".")
	key := make([]int, len(parts))
	for i, p := range parts {
		n, err := pystr.Atoi(p)
		if err != nil {
			// Every caller passes a match of num, whose parts are all
			// digits; anything else sorts before every section.
			n = -1
		}
		key[i] = n
	}
	return key
}

// sortSections orders section numbers as Python sorts their integer lists.
func sortSections(sections []string) {
	slices.SortStableFunc(sections, func(a, b string) int {
		return slices.Compare(sectionKey(a), sectionKey(b))
	})
}

// ancestors lists a section and every section above it, outermost first:
// "5.2.1" gives "5", "5.2", "5.2.1".
func ancestors(section string) []string {
	parts := strings.Split(section, ".")
	out := make([]string, len(parts))
	for i := range parts {
		out[i] = strings.Join(parts[:i+1], ".")
	}
	return out
}

// bands groups a page's lines into rows four points tall, keyed by row, as
// the contents and index parsers read them. Boilerplate lines are left out.
func bands(lines []Line, boiler boilerplate) ([]int, map[int][]Line) {
	rows := map[int][]Line{}
	var keys []int
	for _, ln := range lines {
		if boiler.matches(ln.Text) {
			continue
		}
		key := int(pystr.FloorDiv(ln.Y0, 4))
		if _, ok := rows[key]; !ok {
			keys = append(keys, key)
		}
		rows[key] = append(rows[key], ln)
	}
	slices.Sort(keys)
	for _, key := range keys {
		slices.SortStableFunc(rows[key], func(a, b Line) int { return cmpFloat(a.X0, b.X0) })
	}
	return keys, rows
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// texts is the text of each line.
func texts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = ln.Text
	}
	return out
}
