// Package ingest is the one ingest path. The CLI's ingest and add commands
// and the worker all call Service.Run, so the logic is not forked: who drives
// it and who watches the progress differ, and nothing else does.
//
// A file and a site are the same ingest with different answers to four
// questions, which is why they are two Source implementations rather than two
// pipelines. One transaction boundary, one progress model and one set of
// quality gates serve both.
//
//	           file                        site
//	guard      inside a configured root    scheme, host and address
//	acquire    read from disk              fetch, or a cache hit
//	identity   absolute path               canonical base URL
//	extract    adapter by suffix           walk the nav, parse each page
package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
)

// chunkBatch is how many chunks one transaction inserts. Small enough that a
// cancelled ingest stops promptly, large enough that a 900-chunk manual is
// not 900 transactions.
const chunkBatch = 200

var (
	// ErrCancelled reports an ingest that stopped because its context was
	// cancelled. Nothing it wrote survives.
	ErrCancelled = errors.New("ingest cancelled")
	// ErrUnsupportedFormat reports a source no adapter can read. Refusing it
	// is deterministic, so a worker fails the job rather than retrying it.
	ErrUnsupportedFormat = errors.New("unsupported format")
)

// Extractor turns a file on disk into the normalized intermediate. The file
// source calls it; the port is declared here because reading a document is
// the ingest's dependency however the bytes were obtained.
type Extractor interface {
	// Supports reports whether an adapter exists for the path's suffix.
	Supports(path string) bool
	Extract(ctx context.Context, path string) (*domain.Extraction, error)
}

// StructureError refuses a document whose structure cannot carry a search.
// The ingest writes nothing, and a worker fails the job permanently rather
// than retrying: the next attempt would read the same bytes and refuse them
// again.
type StructureError struct{ Message string }

func (e *StructureError) Error() string { return e.Message }

// Phase names the stage a progress report describes.
type Phase uint8

const (
	PhaseDiscover Phase = iota + 1
	PhaseFetch
	PhaseExtract
	PhaseChunk
	PhaseIndex
)

// The text a phase is stored as, in ingest_jobs.phase, which the server
// reads back and a status tool shows.
func (p Phase) String() string {
	switch p {
	case PhaseDiscover:
		return "discover"
	case PhaseFetch:
		return "fetch"
	case PhaseExtract:
		return "extract"
	case PhaseChunk:
		return "chunk"
	case PhaseIndex:
		return "index"
	default:
		return fmt.Sprintf("phase(%d)", uint8(p))
	}
}

// Progress reports how far one phase has got. total is 0 where nothing knows
// the total yet, which is ordinary while a crawl is still discovering pages.
type Progress func(phase Phase, current, total int)

// report calls a progress function that may be absent.
func (p Progress) report(phase Phase, current, total int) {
	if p != nil {
		p(phase, current, total)
	}
}

// Outcome says what an ingest did to the index.
type Outcome uint8

const (
	// Ingested is a document the index did not hold.
	Ingested Outcome = iota + 1
	// Replaced is a document re-read at an identity the index already held.
	Replaced
	// Unchanged is a source whose bytes are already indexed, under whatever
	// identity they were first read from.
	Unchanged
)

func (o Outcome) String() string {
	switch o {
	case Ingested:
		return "ingested"
	case Replaced:
		return "replaced"
	case Unchanged:
		return "unchanged"
	default:
		return fmt.Sprintf("outcome(%d)", uint8(o))
	}
}

// Source is something that can become one document.
type Source interface {
	// Kind is stored on the document, so a reader knows what it is.
	Kind() domain.SourceKind
	// Identity is what replacement is keyed on: a path, or a canonical base
	// URL.
	Identity() string
	// Acquire guards, then obtains the bytes. It fails rather than returning
	// nothing.
	Acquire(ctx context.Context, progress Progress) error
	// Digest is the content hash a re-ingest is decided by, valid after
	// Acquire.
	Digest() string
	// Extract is the normalized intermediate, after Acquire.
	Extract(ctx context.Context, progress Progress) (*domain.Extraction, error)
}

// Document is the row an ingest creates, before any chunk is written.
type Document struct {
	PageCount *int
	DocID     string
	Title     string
	Format    string
	// Identity is the path or canonical URL the document was read from,
	// stored as documents.source_path.
	Identity string
	Digest   string
	Kind     domain.SourceKind
}

// Existing is a document already in the index.
type Existing struct {
	DocID      string
	Title      string
	ChunkCount int
}

// Ready makes a document visible to search.
type Ready struct {
	// JobID completes a worker's job row in the same transaction, so a
	// document can never be searchable while its job still reads as running.
	// Nil where no job drives the ingest.
	// IngestedAt stamps documents.ingested_at.
	IngestedAt time.Time
	JobID      *int64
	DocID      string
	Warnings   []byte
	ChunkCount int
}

// Repository is where an ingest writes. Each method owns its transaction:
// the service decides what happens, and the repository decides what happens
// atomically.
type Repository interface {
	// ReadyWithDigest is the ready document holding these bytes, or nil.
	ReadyWithDigest(ctx context.Context, digest string) (*Existing, error)
	// DocIDForIdentity is the document already read from this path or URL,
	// or "" where the index holds none.
	DocIDForIdentity(ctx context.Context, identity string) (string, error)
	// DocIDsWithPrefix are the taken identifiers a new one must avoid.
	DocIDsWithPrefix(ctx context.Context, prefix string) ([]string, error)
	// Create writes the document as ingesting, replacing every row of the
	// document it is taking the place of.
	Create(ctx context.Context, doc Document, replacing string) error
	// AddChunks appends one batch.
	AddChunks(ctx context.Context, docID string, chunks []domain.Chunk) error
	// AddPagesAndTerms writes a paginated format's page text and a
	// back-of-book index's terms. Both are empty for most formats.
	AddPagesAndTerms(
		ctx context.Context,
		docID string,
		pages map[int]string,
		terms [][2]string,
	) error
	// MarkReady is the moment the document becomes visible to search.
	MarkReady(ctx context.Context, ready Ready) error
	// Delete removes every trace of a document.
	Delete(ctx context.Context, docID string) error
}

// Result is what one ingest did.
type Result struct {
	Report      *domain.StructureReport
	Diagnostics map[string]any
	DocID       string
	Title       string
	// Note explains an outcome the caller did not ask for, and is empty
	// where the outcome speaks for itself.
	Note       string
	ChunkCount int
	Outcome    Outcome
}

// Options vary one ingest without changing what it does.
type Options struct {
	// Progress is called as each phase advances. Nil reports nothing.
	Progress Progress
	// OnDocID is called once the identifier is settled, before any row is
	// written, so a worker can record it against the job while the ingest
	// runs. Nil where nobody is watching.
	OnDocID func(docID string)
	// JobID completes a job row in the transaction that makes the document
	// visible.
	JobID *int64
	// Title overrides the name the document is given.
	Title string
}

// Service runs ingests against one repository.
type Service struct {
	repo Repository
	now  func() time.Time
}

// New builds the service. now is the clock documents.ingested_at is stamped
// from; nil takes the wall clock.
func New(repo Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repo: repo, now: now}
}

// Run acquires, extracts, chunks and indexes one source.
//
// The document is invisible to search until the final transaction flips
// documents.status to 'ready'.
func (s *Service) Run(ctx context.Context, src Source, opts Options) (*Result, error) {
	if err := src.Acquire(ctx, opts.Progress); err != nil {
		return nil, cancellation(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}

	if same, err := s.repo.ReadyWithDigest(ctx, src.Digest()); err != nil {
		return nil, fmt.Errorf("look up content hash: %w", err)
	} else if same != nil {
		return unchanged(same), nil
	}

	// Replacement is keyed on identity rather than on the title's slug: a
	// retitled source must replace its own rows rather than slugify into a
	// second identifier and orphan the originals.
	replacing, err := s.repo.DocIDForIdentity(ctx, src.Identity())
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", src.Identity(), err)
	}

	prepared, err := s.prepare(ctx, src, opts, replacing)
	if err != nil {
		return nil, err
	}
	if opts.OnDocID != nil {
		opts.OnDocID(prepared.doc.DocID)
	}
	if err := s.write(ctx, prepared, opts); err != nil {
		return nil, err
	}
	return &Result{
		Report:      prepared.report,
		Diagnostics: prepared.extraction.Diagnostics,
		DocID:       prepared.doc.DocID,
		Title:       prepared.doc.Title,
		ChunkCount:  len(prepared.chunks),
		Outcome:     outcomeOf(replacing),
	}, nil
}

// prepared is one source, read and cut, with nothing written yet.
type prepared struct {
	extraction *domain.Extraction
	report     *domain.StructureReport
	chunks     []domain.Chunk
	replacing  string
	doc        Document
}

// prepare extracts, grades and chunks. Every refusal happens here, before a
// row exists, so there is nothing to roll back.
func (s *Service) prepare(
	ctx context.Context,
	src Source,
	opts Options,
	replacing string,
) (*prepared, error) {
	extraction, err := src.Extract(ctx, opts.Progress)
	if err != nil {
		return nil, cancellation(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrCancelled
	}

	report := domain.NewStructureReport(extraction.Diagnostics)
	if report.Fatal() {
		return nil, &StructureError{Message: report.FailureMessage()}
	}

	title := pystr.Strip(firstNonEmpty(opts.Title, extraction.Title, src.Identity()))
	docID, err := s.uniqueDocID(ctx, title, replacing)
	if err != nil {
		return nil, err
	}

	opts.Progress.report(PhaseChunk, 0, 1)
	chunks := domain.Chunks(*extraction)
	if len(chunks) == 0 {
		return nil, &StructureError{Message: noChunksMessage(src.Identity(), report)}
	}
	report.MeasureChunks(chunks)

	return &prepared{
		extraction: extraction,
		report:     report,
		doc: Document{
			PageCount: extraction.PageCount,
			DocID:     docID,
			Title:     title,
			Format:    extraction.Format,
			Identity:  src.Identity(),
			Digest:    src.Digest(),
			Kind:      src.Kind(),
		},
		chunks:    chunks,
		replacing: replacing,
	}, nil
}

// write puts the document in the index and makes it visible. A failure part
// way through leaves nothing behind: the rows written so far are deleted and
// the original error is returned.
func (s *Service) write(ctx context.Context, p *prepared, opts Options) error {
	if err := s.repo.Create(ctx, p.doc, p.replacing); err != nil {
		return fmt.Errorf("create %s: %w", p.doc.DocID, err)
	}
	if err := s.fill(ctx, p, opts); err != nil {
		if delErr := s.repo.Delete(context.WithoutCancel(ctx), p.doc.DocID); delErr != nil {
			return errors.Join(err, fmt.Errorf("clean up %s: %w", p.doc.DocID, delErr))
		}
		return err
	}
	return nil
}

// fill writes everything the document is made of, then flips it to ready.
func (s *Service) fill(ctx context.Context, p *prepared, opts Options) error {
	total := len(p.chunks)
	for start := 0; start < total; start += chunkBatch {
		if err := ctx.Err(); err != nil {
			return ErrCancelled
		}
		end := min(start+chunkBatch, total)
		if err := s.repo.AddChunks(ctx, p.doc.DocID, p.chunks[start:end]); err != nil {
			return fmt.Errorf("write chunks %d to %d: %w", start, end, err)
		}
		opts.Progress.report(PhaseIndex, end, total)
	}

	err := s.repo.AddPagesAndTerms(ctx, p.doc.DocID, p.extraction.Pages, p.extraction.IndexTerms)
	if err != nil {
		return fmt.Errorf("write pages and index terms: %w", err)
	}

	warnings, err := p.report.JSON()
	if err != nil {
		return fmt.Errorf("encode the structure report: %w", err)
	}
	ready := Ready{
		JobID:      opts.JobID,
		Warnings:   warnings,
		DocID:      p.doc.DocID,
		IngestedAt: s.now().UTC(),
		ChunkCount: total,
	}
	if err := s.repo.MarkReady(ctx, ready); err != nil {
		return fmt.Errorf("publish %s: %w", p.doc.DocID, err)
	}
	return nil
}

// uniqueDocID is the identifier a document is filed under: the one it already
// had where it is being replaced, and otherwise its title's slug, numbered
// past whatever is taken.
func (s *Service) uniqueDocID(ctx context.Context, title, replacing string) (string, error) {
	if replacing != "" {
		return replacing, nil
	}
	base := Slugify(title)
	taken, err := s.repo.DocIDsWithPrefix(ctx, base)
	if err != nil {
		return "", fmt.Errorf("look up identifiers beginning %q: %w", base, err)
	}
	return nextFree(base, taken), nil
}

// unchanged is the result for bytes the index already holds.
func unchanged(same *Existing) *Result {
	return &Result{
		DocID:      same.DocID,
		Title:      same.Title,
		ChunkCount: same.ChunkCount,
		Outcome:    Unchanged,
		Note: fmt.Sprintf(
			"content hash already ingested as '%s'; nothing to do", same.DocID),
	}
}

func outcomeOf(replacing string) Outcome {
	if replacing != "" {
		return Replaced
	}
	return Ingested
}

// noChunksMessage explains a document that indexed to nothing.
//
// Such a document is not an empty success. It would hold an identifier,
// report as ready, and never be returned by any query: extraction found
// structure but no text survived, which is a defect worth surfacing rather
// than recording as a valid ingest.
func noChunksMessage(identity string, report *domain.StructureReport) string {
	return fmt.Sprintf(
		"%s: extraction produced no chunks. Structure source was '%s' and %d section "+
			"heading(s) were found, but no body text survived. The document was not indexed.",
		identity, report.StructureSource, report.BodySections)
}

// cancellation reports a cancelled context as ErrCancelled, whatever the
// source called the failure it stopped with.
func cancellation(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ErrCancelled
	}
	return err
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
