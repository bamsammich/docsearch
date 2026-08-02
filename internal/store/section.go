package store

import "strings"

// SectionMatchSQL resolves an index-term section reference to the chunks it
// covers. It is the Go half of one rule that exists in two languages; the
// Python half is docsearch.db.SECTION_MATCH_SQL and the two must agree.
//
// The match is component-wise, never a bare string prefix. A reference to
// chapter "4" covers "4" and "4.1", and must not touch "41" or "43.6.1" --
// LIKE '4%' would sweep in both. The trailing dot is what makes it a boundary.
//
// Coverage metrics are structurally blind to getting this wrong: over-matching
// raises the number of resolved joins, so a "zero unjoinable" check moves in
// the reassuring direction while the answers get worse. Precision needs its
// own tests, and has them in both languages.
const SectionMatchSQL = `(chunks.section = ? OR chunks.section LIKE ? || '.%')`

// SectionCovers reports whether ref covers section, by the same rule as
// SectionMatchSQL. Used to apply the index-term boost in memory once
// candidate chunks have been fetched, so the rule is not reimplemented as an
// ad hoc string comparison at the call site.
func SectionCovers(ref, section string) bool {
	if ref == "" || section == "" {
		return false
	}
	if section == ref {
		return true
	}
	return strings.HasPrefix(section, ref+".")
}

// AnySectionCovers reports whether any ref in refs covers section.
func AnySectionCovers(refs []string, section string) bool {
	for _, ref := range refs {
		if SectionCovers(ref, section) {
			return true
		}
	}
	return false
}
