package domain

// Structural quality findings, and the policy that acts on them.
//
// A worker runs headless, so findings printed to a terminal are lost. They
// are captured as data instead, persisted on the job and the document, and
// surfaced through the status and listing tools.
//
// Ported from python/docsearch/structure.py and the report assembly in
// python/docsearch/ingest.py, which explain each bound with the measurement
// behind it.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/bamsammich/docsearch/internal/pystr"
)

// Quality grades a document's derived structure.
const (
	QualityOK       = "ok"
	QualityDegraded = "degraded"
	QualityFailed   = "failed"
)

// Structure sources, the thing a document's heading tree was derived from.
const (
	// SourceOutline is an embedded outline, which declares which sections
	// exist, how they nest and where each begins. Nothing is inferred, so
	// nothing is required to corroborate it.
	SourceOutline = "outline"
	// SourceFrontTOC, SourceSidebarDOM and SourceIndexPage are equally
	// author-declared but recovered by parsing a printed page or rendered
	// navigation. The parse can misread, so each is checked against what the
	// document turned out to contain, and a disagreement fails.
	SourceFrontTOC   = "front_toc"
	SourceSidebarDOM = "sidebar_dom"
	SourceIndexPage  = "index_page"
	// SourceFontHeuristic is inferred from type sizes with nothing to check
	// it against.
	SourceFontHeuristic = "font_heuristic"
	// SourceUnknown is a report whose source was never recorded.
	SourceUnknown = "unknown"
)

// Bounds the policy grades against.
const (
	// SiteIncompleteFatalShare is the share of a site's known pages that may
	// fail to fetch before the ingest is refused. Provisional: it waits on a
	// doc-site corpus to calibrate against, and is loose on purpose.
	SiteIncompleteFatalShare = 0.20
	// AddressableMin is the distinct heading paths per chunk below which the
	// structure does not separate a document's chunks. Measured at 0.81 to
	// 0.93 where retrieval works and 0.29 on a manual cut into fixed windows.
	AddressableMin = 0.50
	// AddressabilityMinChunks is the chunk count below which a ratio is not a
	// distribution.
	AddressabilityMinChunks = 25
	// HeadlessDegradedRate is the share of chunks without a heading path at
	// which the document is downgraded: one in 944 is a blemish, one in nine
	// is a symptom.
	HeadlessDegradedRate = 0.02
	// UncalibratedScriptNoteShare and UncalibratedScriptDegradedShare bound
	// the share of letters in scripts the token estimator cannot size: noted
	// from the first, downgraded past the second.
	UncalibratedScriptNoteShare     = 0.10
	UncalibratedScriptDegradedShare = 0.50
)

// _listLimit caps how many sections a failure message names.
const _listLimit = 20

// _unreachableLimit caps how many pages a site failure message names.
const _unreachableLimit = 10

// _uncalibratedSamples is roughly how many chunks the script check samples,
// evenly across the document: front matter is often in a different script
// from the body, and a title page proves nothing about the manual behind it.
const _uncalibratedSamples = 200

// StructureReport is what extraction and chunking noticed about a document's
// structure. The JSON field names are the ones persisted in
// documents.warnings, which the MCP server reads back.
type StructureReport struct {
	StructureSource              string   `json:"structure_source"`
	InTOCNotInBody               []string `json:"in_toc_not_in_body"`
	InBodyNotInTOC               []string `json:"in_body_not_in_toc"`
	DetectedMoreThanOnce         []string `json:"detected_more_than_once"`
	ScatteredSections            []string `json:"scattered_sections"`
	CandidatesRejectedByOrdering []string `json:"candidates_rejected_by_ordering"`
	// UnreachablePages and PlacedByPath are site ingest only: pages that
	// produced nothing, and pages no declared hierarchy mentioned, nested by
	// URL path instead.
	UnreachablePages        []string `json:"unreachable_pages"`
	PlacedByPath            []string `json:"placed_by_path"`
	TOCSections             int      `json:"toc_sections"`
	BodySections            int      `json:"body_sections"`
	Chunks                  int      `json:"chunks"`
	DistinctHeadingPaths    int      `json:"distinct_heading_paths"`
	HeadlessChunks          int      `json:"headless_chunks"`
	UncalibratedScriptShare float64  `json:"uncalibrated_script_share"`
	// PagesDeclared and PagesFetched are site ingest only: pages some
	// coverage source said exist, and pages that arrived.
	PagesDeclared int `json:"pages_declared"`
	PagesFetched  int `json:"pages_fetched"`
}

// NewStructureReport reads the report an adapter's diagnostics carry. A
// site's two sides are what its navigation declared and what the crawl
// brought back, the same shape of check a printed table of contents gets
// against the body.
//
// Diagnostics arrive either from an adapter in Go or decoded from JSON, so
// numbers may be ints or float64s and lists []string or []any.
func NewStructureReport(diagnostics map[string]any) *StructureReport {
	source := SourceUnknown
	if s, ok := diagnostics["structure_source"]; ok {
		source = fmt.Sprint(s)
	}
	if site := mapOf(diagnostics["site"]); len(site) > 0 {
		return &StructureReport{
			StructureSource:  source,
			TOCSections:      intOf(site["pages_declared"]),
			BodySections:     intOf(site["pages_fetched"]),
			PagesDeclared:    intOf(site["pages_declared"]),
			PagesFetched:     intOf(site["pages_fetched"]),
			UnreachablePages: stringsOf(site["unreachable"]),
			PlacedByPath:     stringsOf(site["placed_by_path"]),
		}
	}
	xv := mapOf(diagnostics["cross_validation"])
	return &StructureReport{
		StructureSource:              source,
		TOCSections:                  intOf(xv["toc_sections"]),
		BodySections:                 intOf(xv["body_sections"]),
		InTOCNotInBody:               stringsOf(xv["in_toc_not_in_body"]),
		InBodyNotInTOC:               stringsOf(xv["in_body_not_in_toc"]),
		DetectedMoreThanOnce:         stringsOf(xv["detected_more_than_once"]),
		CandidatesRejectedByOrdering: stringsOf(diagnostics["candidates_rejected_by_ordering"]),
	}
}

// MeasureChunks records what the chunks show about the structure. It is
// assessed on the chunks rather than on the derived section list: a source can
// declare plenty of sections and still leave consecutive chunks sharing one
// heading, and it is the chunks a caller filters and reads.
func (r *StructureReport) MeasureChunks(chunks []Chunk) {
	r.ScatteredSections = ScatteredSections(chunks)
	r.Chunks = len(chunks)
	paths := map[string]bool{}
	r.HeadlessChunks = 0
	for _, c := range chunks {
		paths[c.HeadingPath] = true
		if pystr.Strip(c.HeadingPath) == "" {
			r.HeadlessChunks++
		}
	}
	r.DistinctHeadingPaths = len(paths)

	stride := max(1, len(chunks)/_uncalibratedSamples)
	var sample []string
	for i := 0; i < len(chunks); i += stride {
		sample = append(sample, chunks[i].Text)
	}
	r.UncalibratedScriptShare = round3(UncalibratedLetterShare(strings.Join(sample, "\n")))
}

// Validatable reports whether the source is a parsed declaration that can be
// checked against the body.
func (r *StructureReport) Validatable() bool {
	return slices.Contains(
		[]string{SourceFrontTOC, SourceSidebarDOM, SourceIndexPage},
		r.StructureSource,
	)
}

// Authoritative reports whether the source needs no corroboration.
func (r *StructureReport) Authoritative() bool {
	return r.StructureSource == SourceOutline
}

// CrossValidated reports whether a comparison actually happened. Two empty
// sets agree on everything, so a document from which nothing was derived
// would otherwise pass as sound: the absence of evidence, reported as
// evidence.
func (r *StructureReport) CrossValidated() bool {
	return r.Validatable() && r.TOCSections > 0 && r.BodySections > 0
}

// Addressability is distinct heading paths per chunk.
func (r *StructureReport) Addressability() float64 {
	if r.Chunks == 0 {
		return 0
	}
	return float64(r.DistinctHeadingPaths) / float64(r.Chunks)
}

// Unaddressable reports structure that does not separate the document's own
// chunks, whatever produced it: section_filter cannot narrow within it and
// orientation has nothing to work with.
func (r *StructureReport) Unaddressable() bool {
	return r.Chunks >= AddressabilityMinChunks && r.Addressability() < AddressableMin
}

// UnreachableShare is the share of a site's known pages that never arrived.
func (r *StructureReport) UnreachableShare() float64 {
	if r.PagesDeclared == 0 {
		return 0
	}
	return float64(len(r.UnreachablePages)) / float64(r.PagesDeclared)
}

// Incomplete reports a site too little of which arrived for its index to be
// trusted: an index answering from a fraction of a site looks correct in
// every way a caller can see.
func (r *StructureReport) Incomplete() bool {
	return r.PagesDeclared > 0 && r.UnreachableShare() >= SiteIncompleteFatalShare
}

// MostlyHeadless reports enough chunks without a heading path to downgrade
// the whole document.
func (r *StructureReport) MostlyHeadless() bool {
	return r.Chunks > 0 &&
		float64(r.HeadlessChunks)/float64(r.Chunks) >= HeadlessDegradedRate
}

// SymmetricDifference is every section the table of contents and the body
// disagree on, sorted.
func (r *StructureReport) SymmetricDifference() []string {
	set := map[string]bool{}
	for _, s := range slices.Concat(r.InTOCNotInBody, r.InBodyNotInTOC) {
		set[s] = true
	}
	var out []string
	for s := range set {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// Fatal reports findings that refuse the document. A parsed table of contents
// that disagrees with the body is not known to describe the document, and an
// index built on it would answer confidently from the wrong place. A site
// fails when what it declared and what arrived disagree too far.
func (r *StructureReport) Fatal() bool {
	return (r.Validatable() && len(r.SymmetricDifference()) > 0) || r.Incomplete()
}

// Degraded reports non-fatal findings a caller should still be told about.
func (r *StructureReport) Degraded() bool {
	return len(r.DetectedMoreThanOnce) > 0 ||
		len(r.ScatteredSections) > 0 ||
		r.Unaddressable() ||
		r.MostlyHeadless() ||
		r.UncalibratedScriptShare >= UncalibratedScriptDegradedShare
}

// Quality is QualityFailed, QualityDegraded or QualityOK.
func (r *StructureReport) Quality() string {
	switch {
	case r.Fatal():
		return QualityFailed
	case r.Degraded():
		return QualityDegraded
	}
	return QualityOK
}

// Notes are the findings a caller should see, in the caller's terms.
func (r *StructureReport) Notes() []string {
	notes := []string{}
	if len(r.UnreachablePages) > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d of %d known page(s) (%s) could not be fetched and are absent from the index",
			len(r.UnreachablePages), r.PagesDeclared, percent(r.UnreachableShare())))
	}
	if len(r.PlacedByPath) > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d page(s) were named by no navigation source and are nested by their URL "+
				"path instead, which is inferred and has nothing to check it against",
			len(r.PlacedByPath)))
	}
	if r.Unaddressable() {
		notes = append(notes, fmt.Sprintf(
			"%d chunks share only %d distinct heading paths (%.2f per chunk): boundaries "+
				"came from the token budget rather than the document, so section_filter "+
				"cannot narrow within it and outline describes it in %d entries",
			r.Chunks, r.DistinctHeadingPaths, r.Addressability(), r.DistinctHeadingPaths))
	}
	return append(notes, r.scaleNotes()...)
}

// scaleNotes are the notes about sizing and validation, split from Notes to
// keep each list of conditions readable.
func (r *StructureReport) scaleNotes() []string {
	var notes []string
	if r.HeadlessChunks > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d chunk(s) carry no heading path and cannot be reached by heading, filtered, "+
				"or described in an outline", r.HeadlessChunks))
	}
	if r.UncalibratedScriptShare >= UncalibratedScriptNoteShare {
		notes = append(notes, fmt.Sprintf(
			"%s of letters are in a script the token estimator has no calibration for, so "+
				"chunk sizes for this document are in an unknown unit and it may be chunked "+
				"coarser than intended", percent(r.UncalibratedScriptShare)))
	}
	if r.Validatable() && !r.CrossValidated() {
		notes = append(notes, fmt.Sprintf(
			"structure source '%s' was not cross-validated: %d table-of-contents section(s) "+
				"and %d body heading(s) were available to compare",
			r.StructureSource, r.TOCSections, r.BodySections))
	}
	return notes
}

// FailureMessage explains a fatal report to an operator who has no worker
// logs.
func (r *StructureReport) FailureMessage() string {
	if r.Incomplete() {
		shown := r.UnreachablePages[:min(len(r.UnreachablePages), _unreachableLimit)]
		more := ""
		if extra := len(r.UnreachablePages) - _unreachableLimit; extra > 0 {
			more = fmt.Sprintf(" (+%d more)", extra)
		}
		return fmt.Sprintf(
			"completeness gate failed: %d of %d known page(s) (%s) could not be fetched, "+
				"at or above the %s threshold. The site was not navigable enough to index. "+
				"Unreachable: %s%s. The site was not indexed: an index built from a fraction "+
				"of a site answers confidently from the part it happens to hold, and nothing "+
				"a caller can see reveals the rest is missing.",
			len(r.UnreachablePages), r.PagesDeclared, percent(r.UnreachableShare()),
			percent(SiteIncompleteFatalShare), strings.Join(shown, ", "), more)
	}
	parts := []string{fmt.Sprintf(
		"structure validation failed: the %s table of contents and the document body "+
			"disagree on %d section(s).", r.StructureSource, len(r.SymmetricDifference()))}
	if len(r.InTOCNotInBody) > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d section(s) listed in the table of contents were not found as headings in "+
				"the body: %s", len(r.InTOCNotInBody), listed(r.InTOCNotInBody)))
	}
	if len(r.InBodyNotInTOC) > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d heading(s) found in the body are absent from the table of contents: %s",
			len(r.InBodyNotInTOC), listed(r.InBodyNotInTOC)))
	}
	parts = append(parts, "The document was not indexed. Chunks would carry section paths "+
		"that are not known to match the document.")
	return strings.Join(parts, " ")
}

// JSON is the report as persisted in documents.warnings: every field, plus
// the quality grade, the addressability rounded to three places, and the
// notes. Lists are never null and keys are sorted, matching the Python
// pipeline's payload.
func (r *StructureReport) JSON() ([]byte, error) {
	fields, err := json.Marshal(r.withEmptyLists())
	if err != nil {
		return nil, fmt.Errorf("marshal structure report: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(fields, &payload); err != nil {
		return nil, fmt.Errorf("decode structure report fields: %w", err)
	}
	payload["quality"] = r.Quality()
	payload["addressability"] = round3(r.Addressability())
	payload["notes"] = r.Notes()
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal structure report payload: %w", err)
	}
	return raw, nil
}

// withEmptyLists is a copy of the report with every nil list made empty.
func (r *StructureReport) withEmptyLists() StructureReport {
	out := *r
	for _, list := range []*[]string{
		&out.InTOCNotInBody, &out.InBodyNotInTOC, &out.DetectedMoreThanOnce,
		&out.ScatteredSections, &out.CandidatesRejectedByOrdering,
		&out.UnreachablePages, &out.PlacedByPath,
	} {
		if *list == nil {
			*list = []string{}
		}
	}
	return out
}

// ScatteredSections lists sections whose chunks are not contiguous in document
// order. Subdivision yields adjacent ordinals; only a misfired boundary, which
// scatters one section key across the document, leaves gaps.
func ScatteredSections(chunks []Chunk) []string {
	ordinals := map[string][]int{}
	for _, c := range chunks {
		if c.Section != nil && *c.Section != "" {
			ordinals[*c.Section] = append(ordinals[*c.Section], c.Ordinal)
		}
	}
	var out []string
	for section, seen := range ordinals {
		if !contiguous(seen) {
			out = append(out, section)
		}
	}
	slices.Sort(out)
	return out
}

// contiguous reports whether ordinals run without a gap from the first.
func contiguous(ordinals []int) bool {
	for i, o := range ordinals {
		if o != ordinals[0]+i {
			return false
		}
	}
	return true
}

// listed joins up to _listLimit items, marking any cut.
func listed(items []string) string {
	shown := strings.Join(items[:min(len(items), _listLimit)], ", ")
	if len(items) > _listLimit {
		shown += " ..."
	}
	return shown
}

// percent formats a share as Python's `{share:.0%}` does.
func percent(share float64) string {
	return strconv.FormatFloat(share*100, 'f', 0, 64) + "%"
}

// round3 rounds as Python's round(x, 3) does: to the nearest representable
// three-place decimal of the exact binary value, halves to even.
func round3(x float64) float64 {
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 3, 64), 64)
	if err != nil {
		return x
	}
	return rounded
}

// mapOf reads a diagnostics sub-object, or nil when absent or not an object.
func mapOf(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

// intOf reads a diagnostics count, truncating as Python's int() does.
func intOf(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// stringsOf reads a diagnostics list, stringifying its items.
func stringsOf(v any) []string {
	switch list := v.(type) {
	case []string:
		return slices.Clone(list)
	case []any:
		out := make([]string, len(list))
		for i, item := range list {
			out[i] = fmt.Sprint(item)
		}
		return out
	}
	return nil
}
