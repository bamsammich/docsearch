package connectclient

import (
	"math"

	documentv1 "github.com/bamsammich/docsearch/internal/api/docsearch/document/v1"
	ingestv1 "github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/document"
	docingest "github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/job"
)

// Every enum below narrows one proto value to the domain's numbering, which
// the two share by construction: internal/api/connectapi states why, and the
// constant expressions at the bottom of that file stop a build where they
// stop agreeing.
//
// Narrowing rather than casting because the value arrives from a server this
// build did not compile. One it has no name for reads as unset, which every
// report already prints as a dash, rather than wrapping into a grade that
// means something else.
//
//nolint:ireturn // the return is a uint8 enum, not an interface; ireturn reads every type parameter as one.
func enumOf[T ~uint8](v int32) T {
	if v < 0 || v > math.MaxUint8 {
		return 0
	}
	return T(v)
}

func documentOf(d *documentv1.Document) document.Document {
	return document.Document{
		DocID:      d.GetDocId(),
		Title:      d.GetTitle(),
		Format:     d.GetFormat(),
		Status:     d.GetStatus(),
		PageCount:  optionalInt(d.PageCount),
		ChunkCount: optionalInt(d.ChunkCount),
		SourceKind: enumOf[domain.SourceKind](int32(d.GetSourceKind())),
		Quality:    enumOf[domain.Quality](int32(d.GetQuality())),
		Warnings:   d.GetWarnings(),
	}
}

func verifyReportOf(r *documentv1.VerifyReport) *document.VerifyReport {
	findings := make([]domain.Finding, 0, len(r.GetFindings()))
	for _, f := range r.GetFindings() {
		findings = append(findings, domain.Finding{
			Code:     f.GetCode(),
			Detail:   f.GetDetail(),
			Severity: enumOf[domain.Verdict](int32(f.GetSeverity())),
		})
	}
	return &document.VerifyReport{
		Document:           documentOf(r.GetDocument()),
		Problems:           r.GetProblems(),
		Findings:           findings,
		UnjoinableSections: r.GetUnjoinableSections(),
		Measurements:       measurementsOf(r.GetMeasurements()),
		IndexTerms:         int(r.GetIndexTerms()),
		Verdict:            enumOf[domain.Verdict](int32(r.GetVerdict())),
	}
}

func measurementsOf(m *documentv1.Measurements) domain.Measurements {
	return domain.Measurements{
		UncoveredPages:    ints(m.GetUncoveredPages()),
		OrdinalGaps:       ints(m.GetOrdinalGaps()),
		ScatteredSections: m.GetScatteredSections(),
		Longest:           sizedChunksOf(m.GetLongest()),
		Shortest:          sizedChunksOf(m.GetShortest()),
		Tokens: domain.TokenSpread{
			Min:    int(m.GetTokens().GetMin()),
			Median: int(m.GetTokens().GetMedian()),
			P95:    int(m.GetTokens().GetP95()),
			Max:    int(m.GetTokens().GetMax()),
			Mean:   int(m.GetTokens().GetMean()),
			Total:  int(m.GetTokens().GetTotal()),
		},
		MissingLocator:   int(m.GetMissingLocator()),
		ChunksWithImages: int(m.GetChunksWithImages()),
		ChunkCount:       int(m.GetChunkCount()),
	}
}

func inspectReportOf(r *documentv1.InspectReport) *domain.InspectReport {
	findings := make([]domain.InspectFinding, 0, len(r.GetFindings()))
	for _, f := range r.GetFindings() {
		findings = append(findings, domain.InspectFinding{
			Level:  enumOf[domain.Level](int32(f.GetLevel())),
			Label:  f.GetLabel(),
			Detail: f.GetDetail(),
		})
	}
	return &domain.InspectReport{
		Target:          r.GetTarget(),
		Format:          r.GetFormat(),
		PredictedSource: r.GetPredictedSource(),
		PredictedTier:   r.GetPredictedTier(),
		PageCount:       optionalInt(r.PageCount),
		Findings:        findings,
	}
}

func jobOf(j *ingestv1.Job) job.Job {
	return job.Job{
		ProgressCurrent: optionalInt(j.ProgressCurrent),
		ProgressTotal:   optionalInt(j.ProgressTotal),
		Source:          j.GetSource(),
		Title:           j.GetTitle(),
		DocID:           j.GetDocId(),
		Status:          j.GetStatus(),
		Phase:           j.GetPhase(),
		Error:           j.GetError(),
		CreatedAt:       j.GetCreatedAt(),
		UpdatedAt:       j.GetUpdatedAt(),
		Warnings:        j.GetWarnings(),
		JobID:           j.GetJobId(),
		Attempts:        int(j.GetAttempts()),
		Permanent:       j.GetPermanent(),
		Quality:         enumOf[domain.Quality](int32(j.GetQuality())),
	}
}

func resultOf(r *ingestv1.Result) *Result {
	return &Result{
		DocID:       r.GetDocId(),
		Title:       r.GetTitle(),
		Note:        r.GetNote(),
		Outcome:     enumOf[docingest.Outcome](int32(r.GetOutcome())).String(),
		Warnings:    r.GetWarnings(),
		Diagnostics: r.GetDiagnostics(),
		Findings:    r.GetFindings(),
		ChunkCount:  int(r.GetChunkCount()),
		Quality:     enumOf[domain.Quality](int32(r.GetQuality())),
		SourceKind:  enumOf[domain.SourceKind](int32(r.GetSourceKind())),
	}
}

// phaseName is what the progress line calls a phase.
func phaseName(p ingestv1.Phase) string {
	return enumOf[docingest.Phase](int32(p)).String()
}

func sizedChunksOf(chunks []*documentv1.SizedChunk) []domain.SizedChunk {
	out := make([]domain.SizedChunk, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, domain.SizedChunk{
			Ordinal:     int(c.GetOrdinal()),
			Tokens:      int(c.GetTokens()),
			HeadingPath: c.GetHeadingPath(),
		})
	}
	return out
}

func ints(values []int64) []int {
	if len(values) == 0 {
		return nil
	}
	out := make([]int, len(values))
	for i, v := range values {
		out[i] = int(v)
	}
	return out
}

func optionalInt(v *int64) *int {
	if v == nil {
		return nil
	}
	n := int(*v)
	return &n
}
