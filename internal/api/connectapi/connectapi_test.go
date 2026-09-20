package connectapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/api/connectapi"
	"github.com/bamsammich/docsearch/internal/api/connectapi/mocks"
	ingestv1 "github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1/ingestv1connect"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/ingest"
)

// APISuite drives the real Connect stack over HTTP, because what this
// package is for is the wire: a handler asserted by calling its method
// directly would never show a status code or a stream.
type APISuite struct {
	suite.Suite
	ingester *mocks.MockIngester
	sources  *mocks.MockSources
	client   ingestv1connect.IngestServiceClient
}

func TestAPI(t *testing.T) { suite.Run(t, new(APISuite)) }

func (s *APISuite) SetupTest() {
	s.ingester = mocks.NewMockIngester(s.T())
	s.sources = mocks.NewMockSources(s.T())

	path, handler := connectapi.NewServer(s.ingester, s.sources)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	s.T().Cleanup(server.Close)

	s.client = ingestv1connect.NewIngestServiceClient(server.Client(), server.URL)
}

// anySource is what the registry hands back, which this package only passes
// along.
func (s *APISuite) anySource() {
	s.sources.EXPECT().
		For(mock.Anything, mock.Anything).
		Return(nil, nil).
		Maybe()
}

// ingest calls the RPC and collects every message the server sent.
func (s *APISuite) ingest(req *ingestv1.IngestRequest) ([]*ingestv1.IngestResponse, error) {
	stream, err := s.client.Ingest(s.T().Context(), connect.NewRequest(req))
	s.Require().NoError(err)
	var got []*ingestv1.IngestResponse
	for stream.Receive() {
		got = append(got, stream.Msg())
	}
	return got, stream.Err()
}

func (s *APISuite) TestProgressArrivesBeforeTheResult() {
	s.anySource()
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(
			_ context.Context, _ ingest.Source, opts ingest.Options,
		) (*ingest.Result, error) {
			opts.Progress(ingest.PhaseFetch, 3, 10)
			opts.Progress(ingest.PhaseIndex, 200, 200)
			return &ingest.Result{
				DocID: "operator-guide", Title: "Operator Guide",
				ChunkCount: 18, Outcome: ingest.Ingested,
			}, nil
		})

	got, err := s.ingest(&ingestv1.IngestRequest{Source: "/library/guide.md"})
	s.Require().NoError(err)
	s.Require().Len(got, 3)

	first := got[0].GetProgress()
	s.Require().NotNil(first)
	s.Equal(ingestv1.Phase_PHASE_FETCH, first.GetPhase())
	s.Equal(int64(3), first.GetCurrent())
	s.Equal(int64(10), first.GetTotal())

	s.Equal(ingestv1.Phase_PHASE_INDEX, got[1].GetProgress().GetPhase())

	result := got[2].GetResult()
	s.Require().NotNil(result)
	s.Equal("operator-guide", result.GetDocId())
	s.Equal(int64(18), result.GetChunkCount())
	s.Equal(ingestv1.Outcome_OUTCOME_INGESTED, result.GetOutcome())
}

func (s *APISuite) TestTheStructureReportReachesTheCaller() {
	// A client should be able to show what `docsearch verify` would show
	// without asking the server a second question.
	s.anySource()
	report := domain.NewStructureReport(map[string]any{
		"structure_source": "outline",
	})
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		Return(&ingest.Result{
			DocID: "manual", Title: "Manual", ChunkCount: 900,
			Outcome: ingest.Ingested, Report: report,
		}, nil)

	got, err := s.ingest(&ingestv1.IngestRequest{Source: "/library/manual.pdf"})
	s.Require().NoError(err)
	s.Require().Len(got, 1)
	result := got[0].GetResult()
	s.Equal(ingestv1.Quality_QUALITY_OK, result.GetQuality())
	s.Contains(result.GetWarnings(), `"structure_source":"outline"`)
}

func (s *APISuite) TestAnUnchangedSourceCarriesItsNote() {
	s.anySource()
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		Return(&ingest.Result{
			DocID: "guide", Title: "Guide", ChunkCount: 7,
			Outcome: ingest.Unchanged,
			Note:    "content hash already ingested as 'guide'; nothing to do",
		}, nil)

	got, err := s.ingest(&ingestv1.IngestRequest{Source: "/library/copy.md"})
	s.Require().NoError(err)
	result := got[0].GetResult()
	s.Equal(ingestv1.Outcome_OUTCOME_UNCHANGED, result.GetOutcome())
	s.Contains(result.GetNote(), "already ingested as 'guide'")
}

func (s *APISuite) TestAnEmptySourceIsRefusedBeforeAnythingIsAsked() {
	_, err := s.ingest(&ingestv1.IngestRequest{})
	s.Require().Error(err)
	s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))
}

func (s *APISuite) TestARefusedStructureIsNotWorthRetrying() {
	// The same bytes are refused every time, so the caller's document is
	// wrong rather than the server's moment.
	s.anySource()
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &ingest.StructureError{Message: "produced no chunks"})

	_, err := s.ingest(&ingestv1.IngestRequest{Source: "/library/empty.md"})
	s.Require().Error(err)
	s.Equal(connect.CodeFailedPrecondition, connect.CodeOf(err))
	s.Contains(err.Error(), "produced no chunks")
}

func (s *APISuite) TestAFormatNoAdapterReadsIsTheCallersMistake() {
	s.sources.EXPECT().For(mock.Anything, mock.Anything).
		Return(nil, ingest.ErrUnsupportedFormat)

	_, err := s.ingest(&ingestv1.IngestRequest{Source: "/library/archive.zip"})
	s.Require().Error(err)
	s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))
}

func (s *APISuite) TestACancelledIngestIsReportedAsCancelled() {
	s.anySource()
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, ingest.ErrCancelled)

	_, err := s.ingest(&ingestv1.IngestRequest{Source: "/library/guide.md"})
	s.Require().Error(err)
	s.Equal(connect.CodeCanceled, connect.CodeOf(err))
}

func (s *APISuite) TestAFailureNobodyAnticipatedStaysInternal() {
	// A caller learns that retrying might work, and learns nothing about the
	// server's internals beyond the message.
	s.anySource()
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("disk full"))

	_, err := s.ingest(&ingestv1.IngestRequest{Source: "/library/guide.md"})
	s.Require().Error(err)
	s.Equal(connect.CodeInternal, connect.CodeOf(err))
}

func (s *APISuite) TestTheRequestsOptionsReachTheIngest() {
	var seen ingest.Options
	var revalidate bool
	s.sources.EXPECT().For("https://example.com/docs/", false).
		RunAndReturn(func(_ string, r bool) (ingest.Source, error) {
			revalidate = r
			return nil, nil
		})
	s.ingester.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(
			_ context.Context, _ ingest.Source, opts ingest.Options,
		) (*ingest.Result, error) {
			seen = opts
			return &ingest.Result{DocID: "docs", Outcome: ingest.Ingested}, nil
		})

	_, err := s.ingest(&ingestv1.IngestRequest{
		Source:     "https://example.com/docs/",
		Title:      "Widget Docs",
		Revalidate: false,
	})
	s.Require().NoError(err)
	s.Equal("Widget Docs", seen.Title)
	s.False(revalidate, "a re-chunk without requests is the caller's to ask for")
}
