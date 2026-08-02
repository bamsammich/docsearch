package store

import (
	"context"
	"database/sql"
	"strings"
)

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

// TargetChunk is a chunk looked up by expectation rather than by search. Used
// by the evaluation harness to ask whether an answer exists in the index at
// all, independently of how the ranker placed it.
type TargetChunk struct {
	HeadingPath string
	Text        string
}

// ChunksMatchingExpectation returns the chunks a query's expected answer
// covers: a section subtree for a numbered document, a heading substring
// otherwise. Diagnostics only -- no tool calls this.
func (s *Store) ChunksMatchingExpectation(ctx context.Context, docID, expect string,
	bySection bool) ([]TargetChunk, error) {
	if docID == "" || expect == "" {
		return nil, nil
	}
	var (
		rows *sql.Rows
		err  error
	)
	if bySection {
		rows, err = s.db.QueryContext(ctx,
			`SELECT heading_path, text FROM chunks
			  WHERE doc_id = ? AND `+SectionMatchSQL+` ORDER BY ordinal`,
			docID, expect, expect)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT heading_path, text FROM chunks
			  WHERE doc_id = ? AND lower(heading_path) LIKE '%' || lower(?) || '%'
			  ORDER BY ordinal`, docID, expect)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TargetChunk
	for rows.Next() {
		var c TargetChunk
		if err := rows.Scan(&c.HeadingPath, &c.Text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
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
