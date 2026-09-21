package connectapi

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	documentv1 "github.com/bamsammich/docsearch/internal/api/docsearch/document/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/document/v1/documentv1connect"
	typev1 "github.com/bamsammich/docsearch/internal/api/docsearch/type/v1"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/document"
)

// Documents answers questions about what the index holds. The port is
// declared here because this package is what calls it;
// internal/service/document.Service satisfies it.
type Documents interface {
	List(ctx context.Context) ([]document.Document, error)
	Verify(ctx context.Context, docID string) (*document.VerifyReport, error)
	Inspect(ctx context.Context, target string) (*domain.InspectReport, error)
	Remove(ctx context.Context, docID string) error
}

// DocumentServer implements the generated document handler.
type DocumentServer struct {
	documents Documents
	// notFound recognises a document the index does not hold, so a caller
	// asking about one gets NotFound rather than Internal. The repository
	// owns the error; this only has to be told which it is.
	notFound error
}

// NewDocumentServer returns the path to mount the handler on, and the
// handler. notFound is the sentinel the repository returns for a document
// that is not there.
func NewDocumentServer(
	documents Documents,
	notFound error,
	opts ...connect.HandlerOption,
) (string, http.Handler) {
	return documentv1connect.NewDocumentServiceHandler(
		&DocumentServer{documents: documents, notFound: notFound},
		opts...,
	)
}

func (s *DocumentServer) List(
	ctx context.Context,
	_ *connect.Request[documentv1.ListRequest],
) (*connect.Response[documentv1.ListResponse], error) {
	docs, err := s.documents.List(ctx)
	if err != nil {
		return nil, s.asConnectError(err)
	}
	out := make([]*documentv1.Document, len(docs))
	for i, doc := range docs {
		out[i] = documentMessage(doc)
	}
	return connect.NewResponse(&documentv1.ListResponse{Documents: out}), nil
}

func (s *DocumentServer) Verify(
	ctx context.Context,
	req *connect.Request[documentv1.VerifyRequest],
) (*connect.Response[documentv1.VerifyResponse], error) {
	if req.Msg.GetDocId() == "" {
		return nil, connect.NewError(
			connect.CodeInvalidArgument, errors.New("doc_id names the document to verify"))
	}
	report, err := s.documents.Verify(ctx, req.Msg.GetDocId())
	if err != nil {
		return nil, s.asConnectError(err)
	}
	return connect.NewResponse(&documentv1.VerifyResponse{
		Report: verifyMessage(report),
	}), nil
}

func (s *DocumentServer) Inspect(
	ctx context.Context,
	req *connect.Request[documentv1.InspectRequest],
) (*connect.Response[documentv1.InspectResponse], error) {
	if req.Msg.GetTarget() == "" {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("target names the file or site to inspect"))
	}
	report, err := s.documents.Inspect(ctx, req.Msg.GetTarget())
	if err != nil {
		return nil, s.asConnectError(err)
	}
	return connect.NewResponse(&documentv1.InspectResponse{
		Report: inspectMessage(report),
	}), nil
}

func (s *DocumentServer) Remove(
	ctx context.Context,
	req *connect.Request[documentv1.RemoveRequest],
) (*connect.Response[documentv1.RemoveResponse], error) {
	if req.Msg.GetDocId() == "" {
		return nil, connect.NewError(
			connect.CodeInvalidArgument, errors.New("doc_id names the document to remove"))
	}
	if err := s.documents.Remove(ctx, req.Msg.GetDocId()); err != nil {
		return nil, s.asConnectError(err)
	}
	return connect.NewResponse(&documentv1.RemoveResponse{}), nil
}

// asConnectError says what the caller should do about a failure. A document
// the index does not hold is the caller's mistake, not the server's.
func (s *DocumentServer) asConnectError(err error) error {
	if s.notFound != nil && errors.Is(err, s.notFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// documentMessage is one document as a caller sees it.
func documentMessage(doc document.Document) *documentv1.Document {
	return &documentv1.Document{
		DocId:      doc.DocID,
		Title:      doc.Title,
		Format:     doc.Format,
		SourceKind: typev1.SourceKind(doc.SourceKind),
		Status:     doc.Status,
		PageCount:  optionalInt(doc.PageCount),
		ChunkCount: optionalInt(doc.ChunkCount),
		Quality:    typev1.Quality(doc.Quality),
		Warnings:   doc.Warnings,
	}
}

// verifyMessage is a verification report as a caller sees it.
func verifyMessage(report *document.VerifyReport) *documentv1.VerifyReport {
	findings := make([]*documentv1.Finding, len(report.Findings))
	for i, f := range report.Findings {
		findings[i] = &documentv1.Finding{
			Code:     f.Code,
			Severity: typev1.Verdict(f.Severity),
			Detail:   f.Detail,
		}
	}
	return &documentv1.VerifyReport{
		Document:           documentMessage(report.Document),
		Measurements:       measurementsMessage(report.Measurements),
		Problems:           report.Problems,
		Findings:           findings,
		Verdict:            typev1.Verdict(report.Verdict),
		IndexTerms:         int64(report.IndexTerms),
		UnjoinableSections: report.UnjoinableSections,
	}
}

// measurementsMessage is what the chunks showed.
func measurementsMessage(m domain.Measurements) *documentv1.Measurements {
	return &documentv1.Measurements{
		ChunkCount: int64(m.ChunkCount),
		Tokens: &documentv1.TokenSpread{
			Min:    int64(m.Tokens.Min),
			Median: int64(m.Tokens.Median),
			P95:    int64(m.Tokens.P95),
			Max:    int64(m.Tokens.Max),
			Mean:   int64(m.Tokens.Mean),
			Total:  int64(m.Tokens.Total),
		},
		Longest:           sizedChunks(m.Longest),
		Shortest:          sizedChunks(m.Shortest),
		UncoveredPages:    int64s(m.UncoveredPages),
		OrdinalGaps:       int64s(m.OrdinalGaps),
		ScatteredSections: m.ScatteredSections,
		MissingLocator:    int64(m.MissingLocator),
		ChunksWithImages:  int64(m.ChunksWithImages),
	}
}

// inspectMessage is a reconnaissance report as a caller sees it.
func inspectMessage(report *domain.InspectReport) *documentv1.InspectReport {
	findings := make([]*documentv1.InspectFinding, len(report.Findings))
	for i, f := range report.Findings {
		findings[i] = &documentv1.InspectFinding{
			Level:  typev1.Level(f.Level),
			Label:  f.Label,
			Detail: f.Detail,
		}
	}
	return &documentv1.InspectReport{
		Target:          report.Target,
		Format:          report.Format,
		PredictedSource: report.PredictedSource,
		PredictedTier:   report.PredictedTier,
		PageCount:       optionalInt(report.PageCount),
		Findings:        findings,
		Blocked:         report.Blocked(),
	}
}

func sizedChunks(chunks []domain.SizedChunk) []*documentv1.SizedChunk {
	out := make([]*documentv1.SizedChunk, len(chunks))
	for i, c := range chunks {
		out[i] = &documentv1.SizedChunk{
			Ordinal:     int64(c.Ordinal),
			Tokens:      int64(c.Tokens),
			HeadingPath: c.HeadingPath,
		}
	}
	return out
}

func int64s(values []int) []int64 {
	out := make([]int64, len(values))
	for i, v := range values {
		out[i] = int64(v)
	}
	return out
}

func optionalInt(v *int) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

// The proto enums share their numbering with the domain's, so each
// conversion above is a cast. These assertions are what make the cast safe:
// a value renumbered on either side stops the build here rather than
// mislabelling a report in a client.
const (
	_ = uint8(typev1.Verdict_VERDICT_GOOD - typev1.Verdict(domain.VerdictGood))
	_ = uint8(typev1.Verdict_VERDICT_DEGRADED - typev1.Verdict(domain.VerdictDegraded))
	_ = uint8(typev1.Verdict_VERDICT_UNUSABLE - typev1.Verdict(domain.VerdictUnusable))

	_ = uint8(typev1.Level_LEVEL_OK - typev1.Level(domain.LevelOK))
	_ = uint8(typev1.Level_LEVEL_WARN - typev1.Level(domain.LevelWarn))
	_ = uint8(typev1.Level_LEVEL_BLOCKED - typev1.Level(domain.LevelBlocked))
)
