package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// storedReport is the part of a persisted StructureReport a caller is shown.
type storedReport struct {
	Quality              string   `json:"quality"`
	StructureSource      string   `json:"structure_source"`
	InTOCNotInBody       []string `json:"in_toc_not_in_body"`
	InBodyNotInTOC       []string `json:"in_body_not_in_toc"`
	DetectedMoreThanOnce []string `json:"detected_more_than_once"`
	ScatteredSections    []string `json:"scattered_sections"`
	Notes                []string `json:"notes"`
}

// finding is one list of a report, with the phrasing a caller sees for it.
type finding struct {
	label string
	items []string
}

// shownPerFinding is how many subjects of one finding are named before the
// rest are counted.
const shownPerFinding = 10

// Summary reads a stored report back as a caller sees it: its grade, and its
// findings phrased for a person. The worker is headless, so these were
// captured as data at ingest time; this is where a caller finally sees them.
//
// A report that cannot be read is ungraded, the zero Quality, rather than an
// error: the document it describes is still searchable.
func Summary(stored string) (Quality, []string) {
	if stored == "" {
		return 0, nil
	}
	var report storedReport
	if err := json.Unmarshal([]byte(stored), &report); err != nil {
		return 0, nil
	}
	var quality Quality
	if err := quality.UnmarshalText([]byte(report.Quality)); err != nil {
		quality = 0
	}
	var notes []string
	for _, f := range []finding{
		{items: report.InTOCNotInBody,
			label: "sections in the table of contents but not the body"},
		{items: report.InBodyNotInTOC,
			label: "headings in the body but not the table of contents"},
		{items: report.DetectedMoreThanOnce, label: "sections detected more than once"},
		{items: report.ScatteredSections, label: "sections spanning non-adjacent chunks"},
	} {
		if note := f.note(); note != "" {
			notes = append(notes, note)
		}
	}
	// Already phrased for a caller, so they pass through whole rather than
	// being summarised into a count of opaque identifiers.
	return quality, append(notes, report.Notes...)
}

// StoredSource reads which structure source a stored report recorded, or
// SourceUnknown where it recorded none or cannot be read.
func StoredSource(stored string) StructureSource {
	var report storedReport
	if err := json.Unmarshal([]byte(stored), &report); err != nil {
		return SourceUnknown
	}
	source, err := ParseStructureSource(report.StructureSource)
	if err != nil {
		return SourceUnknown
	}
	return source
}

// note renders one finding, naming at most shownPerFinding of its subjects.
func (f finding) note() string {
	if len(f.items) == 0 {
		return ""
	}
	shown := f.items[:min(len(f.items), shownPerFinding)]
	note := fmt.Sprintf("%d %s: %s", len(f.items), f.label, strings.Join(shown, ", "))
	if len(f.items) > len(shown) {
		note += fmt.Sprintf(" (+%d more)", len(f.items)-len(shown))
	}
	return note
}
