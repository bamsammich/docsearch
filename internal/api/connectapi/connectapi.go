// Package connectapi is the ConnectRPC API onto the ingest service.
//
// Claude speaks MCP and cannot be asked to speak anything else, so
// internal/api/mcpapi stays. Every other client comes through here: the
// docsearch CLI, docsearch-sync beside Paperless, and any later web UI
// through Connect-Web.
//
// Nothing here decides anything about a document. It turns one request into
// one call on the ingest service, turns what comes back into messages, and
// turns a refusal into the Connect code that says what the caller should do
// about it.
package connectapi

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	ingestv1 "github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1/ingestv1connect"
	typev1 "github.com/bamsammich/docsearch/internal/api/docsearch/type/v1"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest"
)

// Ingester runs one ingest. The port is declared here because this package
// is what calls it; internal/service/ingest.Service satisfies it.
type Ingester interface {
	Run(ctx context.Context, src ingest.Source, opts ingest.Options) (*ingest.Result, error)
}

// Sources turns the string a caller sent into something that can be read.
//
// Whether a source is a path inside the library root or a URL worth crawling
// is a policy question with a different answer per deployment, so the
// decision belongs to whoever builds the server rather than to its API.
type Sources interface {
	For(source string, revalidate bool) (ingest.Source, error)
}

// Server implements the generated handler.
type Server struct {
	ingester Ingester
	sources  Sources
}

// NewServer returns the path to mount the handler on, and the handler.
func NewServer(
	ingester Ingester,
	sources Sources,
	opts ...connect.HandlerOption,
) (string, http.Handler) {
	return ingestv1connect.NewIngestServiceHandler(
		&Server{ingester: ingester, sources: sources},
		opts...,
	)
}

// Ingest reads one source, sending progress as it goes and the result once,
// last.
func (s *Server) Ingest(
	ctx context.Context,
	req *connect.Request[ingestv1.IngestRequest],
	stream *connect.ServerStream[ingestv1.IngestResponse],
) error {
	msg := req.Msg
	if msg.GetSource() == "" {
		return connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("source names the file or site to read, and was empty"),
		)
	}
	source, err := s.sources.For(msg.GetSource(), msg.GetRevalidate())
	if err != nil {
		return asConnectError(err)
	}

	// A progress message that cannot be sent is the caller having hung up,
	// so the ingest is cancelled rather than left running for nobody.
	ctx, hungUp := context.WithCancel(ctx)
	defer hungUp()

	result, err := s.ingester.Run(ctx, source, ingest.Options{
		Title:    msg.GetTitle(),
		Progress: sendProgress(ctx, stream, hungUp),
	})
	if err != nil {
		return asConnectError(err)
	}
	return stream.Send(&ingestv1.IngestResponse{
		Message: &ingestv1.IngestResponse_Result{Result: resultMessage(result)},
	})
}

// sendProgress reports each phase to the caller, and calls hungUp once a
// message cannot be delivered.
//
// Progress reports nothing back, so a failed send has nowhere to go as a
// return value. Cancelling is what it means: the caller is gone, and the
// ingest it asked for should stop rather than finish for nobody. The service
// then returns ErrCancelled, which the caller would have seen anyway.
func sendProgress(
	ctx context.Context,
	stream *connect.ServerStream[ingestv1.IngestResponse],
	hungUp context.CancelFunc,
) ingest.Progress {
	return func(phase ingest.Phase, current, total int) {
		if ctx.Err() != nil {
			return
		}
		err := stream.Send(&ingestv1.IngestResponse{
			Message: &ingestv1.IngestResponse_Progress{
				Progress: &ingestv1.Progress{
					Phase:   ingestv1.Phase(phase),
					Current: int64(current),
					Total:   int64(total),
				},
			},
		})
		if err != nil {
			hungUp()
		}
	}
}

// resultMessage is one ingest's result as the caller sees it.
func resultMessage(result *ingest.Result) *ingestv1.Result {
	msg := &ingestv1.Result{
		DocId:      result.DocID,
		Title:      result.Title,
		ChunkCount: int64(result.ChunkCount),
		Outcome:    ingestv1.Outcome(result.Outcome),
		Note:       result.Note,
	}
	if result.Report == nil {
		return msg
	}
	msg.Quality = typev1.Quality(result.Report.Quality())
	if warnings, err := result.Report.JSON(); err == nil {
		msg.Warnings = string(warnings)
	}
	return msg
}

// asConnectError says what the caller should do about a refusal.
//
// The distinction that matters is whether trying again could work.
// A structure refusal reads the same bytes every time, so it is the caller's
// document that is wrong rather than the server's moment.
func asConnectError(err error) error {
	var structure *ingest.StructureError
	switch {
	case errors.Is(err, ingest.ErrCancelled), errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.As(err, &structure):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, ingest.ErrUnsupportedFormat):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// The proto enums share their numbering with the domain's, so each
// conversion above is a cast. These assertions are what make the cast safe:
// a value renumbered on either side stops the build here rather than
// mislabelling a document's grade in a client.
const (
	_ = uint8(typev1.Quality_QUALITY_OK - typev1.Quality(domain.QualityOK))
	_ = uint8(typev1.Quality_QUALITY_DEGRADED - typev1.Quality(domain.QualityDegraded))
	_ = uint8(typev1.Quality_QUALITY_FAILED - typev1.Quality(domain.QualityFailed))

	_ = uint8(typev1.SourceKind_SOURCE_KIND_FILE - typev1.SourceKind(domain.SourceKindFile))
	_ = uint8(typev1.SourceKind_SOURCE_KIND_SITE - typev1.SourceKind(domain.SourceKindSite))

	_ = uint8(ingestv1.Phase_PHASE_DISCOVER - ingestv1.Phase(ingest.PhaseDiscover))
	_ = uint8(ingestv1.Phase_PHASE_FETCH - ingestv1.Phase(ingest.PhaseFetch))
	_ = uint8(ingestv1.Phase_PHASE_EXTRACT - ingestv1.Phase(ingest.PhaseExtract))
	_ = uint8(ingestv1.Phase_PHASE_CHUNK - ingestv1.Phase(ingest.PhaseChunk))
	_ = uint8(ingestv1.Phase_PHASE_INDEX - ingestv1.Phase(ingest.PhaseIndex))

	_ = uint8(ingestv1.Outcome_OUTCOME_INGESTED - ingestv1.Outcome(ingest.Ingested))
	_ = uint8(ingestv1.Outcome_OUTCOME_REPLACED - ingestv1.Outcome(ingest.Replaced))
	_ = uint8(ingestv1.Outcome_OUTCOME_UNCHANGED - ingestv1.Outcome(ingest.Unchanged))
)
