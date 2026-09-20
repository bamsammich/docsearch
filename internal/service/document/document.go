// Package document answers questions about what the index already holds.
//
// Verification is the one worth naming. It asks two different questions and
// keeps them apart: whether the rows are consistent with each other and with
// the document they came from, and whether the chunks are shaped so search
// can work on them. A document can fail either while passing the other, and
// a report that conflated them would say a consistent document is fine.
//
// Ported from python/docsearch/verify.py and cli.py.
package document

import (
	"context"
	"fmt"

	"github.com/bamsammich/docsearch/internal/domain"
)

// Document is one document the index holds.
type Document struct {
	PageCount  *int
	ChunkCount *int
	DocID      string
	Title      string
	Format     string
	Status     string
	Warnings   []string
	// SourceKind is unset where the index holds a kind this build does not
	// know, which is a database from a newer build rather than a defect.
	SourceKind domain.SourceKind
	Quality    domain.Quality
}

// Repository is what the index can be asked. The port is declared here
// because this package is what asks.
type Repository interface {
	// Get is one document, whatever its status: verification has to be able
	// to look at a document that is not ready.
	Get(ctx context.Context, docID string) (*Document, error)
	List(ctx context.Context) ([]Document, error)
	// Chunks are a document's chunks in ordinal order.
	Chunks(ctx context.Context, docID string) ([]domain.Chunk, error)
	// IndexTermSections are the distinct sections a document's back-of-book
	// index points at.
	IndexTermSections(ctx context.Context, docID string) ([]string, error)
	// SectionHasChunks reports whether a section, or any section beneath it,
	// holds a chunk.
	SectionHasChunks(ctx context.Context, docID, section string) (bool, error)
	// Delete removes every trace of a document.
	Delete(ctx context.Context, docID string) error
}

// Inspector reports what ingest would make of a target, writing nothing.
type Inspector interface {
	Inspect(ctx context.Context, target string) (*domain.InspectReport, error)
}

// VerifyReport is what verification found.
type VerifyReport struct {
	Document Document `json:"document"`
	// Problems are integrity failures: the rows disagree with each other or
	// with the document. Each one is a defect in what was stored.
	Problems []string `json:"problems"`
	// Findings are quality defects, every one of them compatible with a
	// clean ingest that reached 'ready'.
	Findings     []domain.Finding    `json:"findings"`
	Measurements domain.Measurements `json:"measurements"`
	// Verdict grades the chunks. Integrity is reported in Problems: the two
	// answer different questions and a document can fail either.
	Verdict domain.Verdict `json:"verdict"`
}

// Service answers questions about stored documents.
type Service struct {
	repo      Repository
	inspector Inspector
}

// New builds the service over the index and the reconnaissance.
func New(repo Repository, inspector Inspector) *Service {
	return &Service{repo: repo, inspector: inspector}
}

// List is every ready document.
func (s *Service) List(ctx context.Context) ([]Document, error) {
	docs, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	return docs, nil
}

// Remove deletes a document from the index.
func (s *Service) Remove(ctx context.Context, docID string) error {
	if _, err := s.repo.Get(ctx, docID); err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, docID); err != nil {
		return fmt.Errorf("remove %s: %w", docID, err)
	}
	return nil
}

// Inspect reports what ingest would make of a target.
func (s *Service) Inspect(ctx context.Context, target string) (*domain.InspectReport, error) {
	return s.inspector.Inspect(ctx, target)
}

// Verify checks one stored document, both ways.
func (s *Service) Verify(ctx context.Context, docID string) (*VerifyReport, error) {
	doc, err := s.repo.Get(ctx, docID)
	if err != nil {
		return nil, err
	}
	chunks, err := s.repo.Chunks(ctx, docID)
	if err != nil {
		return nil, fmt.Errorf("read the chunks of %s: %w", docID, err)
	}

	report := &VerifyReport{Document: *doc, Problems: []string{}, Findings: []domain.Finding{}}
	if len(chunks) == 0 {
		report.Problems = append(report.Problems, "document has no chunks")
		report.Verdict = domain.VerdictGood
		return report, nil
	}

	report.Measurements = domain.Measure(chunks, doc.PageCount)
	facts := make([]domain.ChunkFacts, len(chunks))
	for i, c := range chunks {
		facts[i] = domain.FactsOf(c)
	}
	report.Findings = domain.Grade(facts)
	report.Verdict = domain.GradeVerdict(report.Findings)

	unjoinable, err := s.unjoinableSections(ctx, docID)
	if err != nil {
		return nil, err
	}
	report.Problems = append(report.Problems, problemsOf(report.Measurements, unjoinable)...)
	return report, nil
}

// unjoinableSections are the sections a back-of-book index points at that no
// chunk answers for.
//
// Matched as a subtree, the way a section filter matches: an index entry
// pointing at chapter 4 refers to the whole chapter, and a chapter whose
// preamble folded into its first child has no chunk of its own.
func (s *Service) unjoinableSections(ctx context.Context, docID string) ([]string, error) {
	sections, err := s.repo.IndexTermSections(ctx, docID)
	if err != nil {
		return nil, fmt.Errorf("read the index terms of %s: %w", docID, err)
	}
	var unjoinable []string
	for _, section := range sections {
		found, err := s.repo.SectionHasChunks(ctx, docID, section)
		if err != nil {
			return nil, fmt.Errorf("resolve section %s of %s: %w", section, docID, err)
		}
		if !found {
			unjoinable = append(unjoinable, section)
		}
	}
	return unjoinable, nil
}

// problemsOf names each integrity failure in what was measured.
func problemsOf(m domain.Measurements, unjoinable []string) []string {
	var problems []string
	if len(m.OrdinalGaps) > 0 {
		problems = append(problems, fmt.Sprintf(
			"chunk ordinals skip at %v; a batch was lost between transactions and every "+
				"read path steps over it silently", m.OrdinalGaps))
	}
	if len(m.UncoveredPages) > 0 {
		problems = append(problems, fmt.Sprintf(
			"%d page(s) are claimed by no chunk, starting at %d; text was extracted and "+
				"never stored, so no search returns it",
			len(m.UncoveredPages), m.UncoveredPages[0]))
	}
	if m.MissingLocator > 0 {
		problems = append(problems, fmt.Sprintf(
			"%d chunk(s) of a paginated document name no page, so a result cannot cite "+
				"where it came from", m.MissingLocator))
	}
	if len(m.ScatteredSections) > 0 {
		problems = append(problems, fmt.Sprintf(
			"section(s) %v are split across non-adjacent chunks; a boundary misfired and "+
				"the index term join no longer resolves them", m.ScatteredSections))
	}
	if len(unjoinable) > 0 {
		problems = append(problems, fmt.Sprintf(
			"index term(s) point at section(s) %v that no chunk answers for, so those "+
				"entries resolve to nothing", unjoinable))
	}
	return problems
}
