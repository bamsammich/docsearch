package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/bamsammich/docsearch/internal/store/dbgen"
)

// SectionCovers reports whether ref covers section: the in-memory half of a
// rule that also exists as SQL, in the ChunksInSection query and in
// docsearch.db.SECTION_MATCH_SQL. All three must agree.
//
// The match is component-wise, never a bare string prefix. A reference to
// chapter "4" covers "4" and "4.1", and must not touch "41" or "43.6.1" --
// LIKE '4%' would sweep in both. The trailing dot is what makes it a boundary.
//
// Coverage metrics are structurally blind to getting this wrong: over-matching
// raises the number of resolved joins, so a "zero unjoinable" check moves in
// the reassuring direction while the answers get worse. Precision needs its
// own tests, and has them in both languages.
//
// Applied once candidate chunks have been fetched, so the rule is not
// reimplemented as an ad hoc string comparison at the call site.
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
	var out []TargetChunk
	if bySection {
		ref := sql.NullString{String: expect, Valid: true}
		rows, err := s.q.ChunksInSection(ctx, dbgen.ChunksInSectionParams{
			DocID: docID, Section: ref, Column3: ref,
		})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, TargetChunk{HeadingPath: r.HeadingPath, Text: r.Text})
		}
		return out, nil
	}
	rows, err := s.q.ChunksMatchingHeading(ctx, dbgen.ChunksMatchingHeadingParams{
		DocID: docID, LOWER: expect,
	})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, TargetChunk{HeadingPath: r.HeadingPath, Text: r.Text})
	}
	return out, nil
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

// SampledChunk is one chunk drawn for a self-labelled retrieval probe.
type SampledChunk struct {
	ChunkID     int64
	HeadingPath string
	Text        string
}

// SampleChunks draws every stride-th chunk of a document in ordinal order.
//
// Deliberately a fixed stride rather than a random sample: the probe is a
// regression check, and a result that moves because the sample moved cannot be
// compared against the run before it. Stride also spreads the draw across the
// whole document, where taking the first N would characterise only the front
// matter.
func (s *Store) SampleChunks(ctx context.Context, docID string, stride int) ([]SampledChunk, error) {
	if stride < 1 {
		stride = 1
	}
	rows, err := s.q.AllChunksInOrder(ctx, docID)
	if err != nil {
		return nil, err
	}

	var out []SampledChunk
	for i, r := range rows {
		if i%stride == 0 {
			out = append(out, SampledChunk{
				ChunkID: r.ID, HeadingPath: r.HeadingPath, Text: r.Text,
			})
		}
	}
	return out, nil
}
