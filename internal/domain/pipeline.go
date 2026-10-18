package domain

import "encoding/json"

// PipelineVersion names the extraction, chunking and grading code that reads
// a document. Every structure report records the version that wrote it, and
// an ingest re-reads bytes the index already holds when their report is
// older than this build.
//
// Bump it by hand in any change to what an adapter extracts, how chunks are
// cut, or how a structure report grades. Each bump re-reads every held
// document the next time its bytes are ingested, once.
//
// Version 1 is the first recorded. Every report written before it has no
// version, which reads as 0, so the long-section grading of #106 reaches
// those documents on their next ingest.
const PipelineVersion = 1

// storedVersion is the one field of a persisted report that
// StoredPipelineVersion reads.
type storedVersion struct {
	PipelineVersion int `json:"pipeline_version"`
}

// StoredPipelineVersion is the version a persisted structure report records.
// A report written before versions were recorded, or one that does not
// decode, is 0: older than any build, so its document is read again rather
// than trusted.
func StoredPipelineVersion(warnings string) int {
	var stored storedVersion
	if err := json.Unmarshal([]byte(warnings), &stored); err != nil {
		return 0
	}
	return stored.PipelineVersion
}
