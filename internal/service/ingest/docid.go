package ingest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// nonSlug is every run of characters a document identifier cannot hold.
var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// stripMarks decomposes a string and drops the combining marks, which is
// Python's NFKD normalize followed by an ASCII encode that ignores what it
// cannot represent: "Café Brûlé" becomes "Cafe Brule" rather than "Caf Brl".
var stripMarks = transform.Chain(
	norm.NFKD,
	runes.Remove(runes.In(unicode.Mn)),
	norm.NFC,
)

// Slugify turns a title into the identifier a document is filed under. A
// title with nothing a slug can hold, which a CJK manual's often is, becomes
// "document" and is then numbered past whatever is taken.
func Slugify(title string) string {
	folded, _, err := transform.String(stripMarks, title)
	if err != nil {
		folded = title
	}
	var ascii strings.Builder
	for _, r := range strings.ToLower(folded) {
		if r < 128 {
			ascii.WriteRune(r)
		}
	}
	slug := strings.Trim(nonSlug.ReplaceAllString(ascii.String(), "-"), "-")
	if slug == "" {
		return "document"
	}
	return slug
}

// nextFree is base, or base numbered from 2 past every identifier taken.
func nextFree(base string, taken []string) string {
	if !slices.Contains(taken, base) {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if !slices.Contains(taken, candidate) {
			return candidate
		}
	}
}
