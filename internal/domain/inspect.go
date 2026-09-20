package domain

import "strings"

// Level is how much a reconnaissance finding matters.
type Level uint8

const (
	// LevelOK is a question answered well.
	LevelOK Level = iota + 1
	// LevelWarn is a document that will ingest and will search worse for
	// what the finding names.
	LevelWarn
	// LevelBlocked is a document ingest would refuse, or should.
	LevelBlocked
)

var levelText = newEnumText("level", map[Level]string{
	LevelOK:      "ok",
	LevelWarn:    "warn",
	LevelBlocked: "blocked",
})

func (l Level) String() string                { return levelText.name(l) }
func (l Level) MarshalText() ([]byte, error)  { return levelText.marshal(l) }
func (l *Level) UnmarshalText(b []byte) error { return levelText.unmarshal(b, l) }

// InspectFinding is one question asked of a document before ingesting it.
type InspectFinding struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
	Level  Level  `json:"level"`
}

// InspectReport is what reconnaissance made of a target.
//
// Whether a document will be searchable is decided by what structure can be
// derived from it, and that is knowable before anything is written. The
// report says so, so a document that will fail, or will succeed badly, says
// so before it costs a worker an hour.
type InspectReport struct {
	// Target is a path for a file, a seed URL for a site.
	Target string `json:"target"`
	Format string `json:"format"`
	// PredictedSource is the structure source ingest would end up using.
	PredictedSource string `json:"predicted_source"`
	// PredictedTier says how much that source can be trusted: declared and
	// checkable, declared and recovered by parsing, or inferred with nothing
	// to check it against.
	PredictedTier string `json:"predicted_tier"`
	// PageCount is a PDF's pages or a site's fetched pages, and is nil for a
	// format with neither.
	PageCount *int             `json:"page_count"`
	Findings  []InspectFinding `json:"findings"`
}

// Blocked reports a target ingest would refuse.
func (r *InspectReport) Blocked() bool {
	for _, f := range r.Findings {
		if f.Level == LevelBlocked {
			return true
		}
	}
	return false
}

// Display is the target as a person refers to it: a site by its URL, a file
// by its name rather than its whole path.
func (r *InspectReport) Display() string {
	if strings.Contains(r.Target, "://") {
		return r.Target
	}
	if i := strings.LastIndexByte(r.Target, '/'); i >= 0 {
		return r.Target[i+1:]
	}
	return r.Target
}

// Add appends one finding.
func (r *InspectReport) Add(level Level, label, detail string) {
	r.Findings = append(r.Findings, InspectFinding{
		Label: label, Detail: detail, Level: level,
	})
}

// The tiers a predicted source falls into, named once so a report and the
// structure policy cannot describe the same source differently.
const (
	TierAuthoritative = "authoritative"
	TierDeclared      = "declared, recovered by parsing"
	TierInferred      = "inferred, no source to check it against"
)
