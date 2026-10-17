package connectclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/api/connectclient"
	documentv1 "github.com/bamsammich/docsearch/internal/api/docsearch/document/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/document/v1/documentv1connect"
	ingestv1 "github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1/ingestv1connect"
	typev1 "github.com/bamsammich/docsearch/internal/api/docsearch/type/v1"
	"github.com/bamsammich/docsearch/internal/domain"
)

// ClientSuite drives the real Connect stack over HTTP against a server that
// answers with fixed messages, so what is under test is the conversion and
// the headers rather than anything a mock was told to do.
type ClientSuite struct {
	suite.Suite
	client *connectclient.Client
	// seen is the Authorization header of the last request to arrive.
	seen string
}

func TestClient(t *testing.T) { suite.Run(t, new(ClientSuite)) }

func (s *ClientSuite) SetupTest() {
	mux := http.NewServeMux()
	mux.Handle(documentv1connect.NewDocumentServiceHandler(&stubDocuments{}))
	mux.Handle(ingestv1connect.NewIngestServiceHandler(&stubIngest{}))
	mux.Handle(ingestv1connect.NewJobServiceHandler(&stubJobs{}))

	server := httptest.NewServer(s.recordAuthorization(mux))
	s.T().Cleanup(server.Close)
	s.client = connectclient.New(server.Client(), server.URL, "secret-token")
}

// recordAuthorization keeps the token the client sent, so that a call which
// forgets it fails here rather than in production.
func (s *ClientSuite) recordAuthorization(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.seen = r.Header.Get("Authorization")
		next.ServeHTTP(w, r)
	})
}

func (s *ClientSuite) TestAListedDocumentKeepsItsTypedGrade() {
	docs, err := s.client.List(s.T().Context())
	s.Require().NoError(err)
	s.Require().Len(docs, 1)
	s.Equal("manual", docs[0].DocID)
	s.Equal(domain.QualityDegraded, docs[0].Quality)
	s.Equal(domain.SourceKindSite, docs[0].SourceKind)
	s.Require().NotNil(docs[0].PageCount)
	s.Equal(318, *docs[0].PageCount)
	s.Equal("Bearer secret-token", s.seen)
}

func (s *ClientSuite) TestAnAbsentCountArrivesAbsentRatherThanZero() {
	// A Markdown document has no pages, which is not a page count of zero.
	docs, err := s.client.List(s.T().Context())
	s.Require().NoError(err)
	s.Nil(docs[0].ChunkCount)
}

func (s *ClientSuite) TestAVerifyReportArrivesWholeEnoughToPrint() {
	report, err := s.client.Verify(s.T().Context(), "manual")
	s.Require().NoError(err)
	s.Equal(880, report.IndexTerms)
	s.Equal([]string{"7. Troubleshooting"}, report.UnjoinableSections)
	s.Equal(domain.VerdictDegraded, report.Verdict)
	s.Require().Len(report.Findings, 1)
	s.Equal(domain.VerdictDegraded, report.Findings[0].Severity)
	s.Equal(412, report.Measurements.ChunkCount)
	s.Equal(225776, report.Measurements.Tokens.Total)
	s.Contains(report.Display(), "index       880 terms; 1 unjoinable")
}

func (s *ClientSuite) TestAnInspectReportKnowsItIsBlockedFromItsFindings() {
	// The wire carries `blocked` as a computed field; the domain recomputes
	// it from the findings, so the two must agree without it being read.
	report, err := s.client.Inspect(s.T().Context(), "scan.pdf")
	s.Require().NoError(err)
	s.True(report.Blocked())
	s.Contains(report.Report(), "This document cannot be ingested as it stands.")
}

func (s *ClientSuite) TestEnqueueNamesEveryJobItQueued() {
	queued, err := s.client.Enqueue(s.T().Context(), "/library", "")
	s.Require().NoError(err)
	s.Require().Len(queued, 2)
	s.Equal("/library/b.pdf", queued[1].Source)
	s.Equal(int64(2), queued[1].JobID)
	s.Equal(2, queued[1].Position)
}

func (s *ClientSuite) TestAQueuedJobArrivesWithoutProgress() {
	jobs, err := s.client.Jobs(s.T().Context(), false, 0)
	s.Require().NoError(err)
	s.Require().Len(jobs, 1)
	s.Nil(jobs[0].ProgressCurrent, "a job that has not started has no progress")
	s.Equal("queued", jobs[0].Status)
}

func (s *ClientSuite) TestAnIngestReportsEveryPhaseAndThenItsResult() {
	var phases []string
	result, err := s.client.Ingest(s.T().Context(), "/library/guide.md", "Guide", true,
		func(p connectclient.Progress) { phases = append(phases, p.Phase) })
	s.Require().NoError(err)
	s.Equal([]string{"fetch", "index"}, phases)
	s.Equal("guide", result.DocID)
	s.Equal("ingested", result.Outcome)
	s.Equal([]string{"2 chunks carry no heading path"}, result.Findings)
	// The streaming call has to carry the token too: an interceptor that
	// only wraps unary calls would authenticate every command except the
	// one that writes documents.
	s.Equal("Bearer secret-token", s.seen)
}

func (s *ClientSuite) TestAStreamThatEndsWithoutAResultIsAnError() {
	_, err := s.client.Ingest(s.T().Context(), "silent", "", true, nil)
	s.Require().Error(err)
	s.Contains(err.Error(), "without a result")
}

func (s *ClientSuite) TestCancellingReportsWhatTheJobReadsAsNow() {
	status, err := s.client.Cancel(s.T().Context(), 7)
	s.Require().NoError(err)
	s.Equal("cancelling", status)
}

// stubDocuments answers with one document, one report of each kind.
type stubDocuments struct{}

func (*stubDocuments) List(
	context.Context, *connect.Request[documentv1.ListRequest],
) (*connect.Response[documentv1.ListResponse], error) {
	pages := int64(318)
	return connect.NewResponse(&documentv1.ListResponse{
		Documents: []*documentv1.Document{{
			DocId:      "manual",
			Title:      "A Manual",
			Format:     "pdf",
			Status:     "ready",
			PageCount:  &pages,
			Quality:    typev1.Quality_QUALITY_DEGRADED,
			SourceKind: typev1.SourceKind_SOURCE_KIND_SITE,
			Warnings:   []string{"21 pages are covered by no chunk"},
		}},
	}), nil
}

func (*stubDocuments) Verify(
	context.Context, *connect.Request[documentv1.VerifyRequest],
) (*connect.Response[documentv1.VerifyResponse], error) {
	return connect.NewResponse(&documentv1.VerifyResponse{
		Report: &documentv1.VerifyReport{
			Document: &documentv1.Document{DocId: "manual", Format: "pdf", Status: "ready"},
			Measurements: &documentv1.Measurements{
				ChunkCount: 412,
				Tokens:     &documentv1.TokenSpread{Total: 225776, Mean: 548},
			},
			Problems: []string{"1 index section joins no chunk"},
			Findings: []*documentv1.Finding{
				{
					Code:     "oversized",
					Severity: typev1.Verdict_VERDICT_DEGRADED,
					Detail:   "41 chunks exceed 1,200 tokens",
				},
			},
			Verdict:            typev1.Verdict_VERDICT_DEGRADED,
			IndexTerms:         880,
			UnjoinableSections: []string{"7. Troubleshooting"},
		},
	}), nil
}

func (*stubDocuments) Inspect(
	context.Context, *connect.Request[documentv1.InspectRequest],
) (*connect.Response[documentv1.InspectResponse], error) {
	return connect.NewResponse(&documentv1.InspectResponse{
		Report: &documentv1.InspectReport{
			Target:          "scan.pdf",
			Format:          "pdf",
			PredictedSource: "unknown",
			PredictedTier:   domain.TierInferred,
			Findings: []*documentv1.InspectFinding{{
				Level:  typev1.Level_LEVEL_BLOCKED,
				Label:  "text layer",
				Detail: "absent on every page",
			}},
			Blocked: true,
		},
	}), nil
}

func (*stubDocuments) Remove(
	context.Context, *connect.Request[documentv1.RemoveRequest],
) (*connect.Response[documentv1.RemoveResponse], error) {
	return connect.NewResponse(&documentv1.RemoveResponse{}), nil
}

// stubIngest streams two progress messages and one result, unless asked for
// the source that ends the stream saying nothing.
type stubIngest struct{}

func (*stubIngest) Ingest(
	_ context.Context,
	req *connect.Request[ingestv1.IngestRequest],
	stream *connect.ServerStream[ingestv1.IngestResponse],
) error {
	if req.Msg.GetSource() == "silent" {
		return nil
	}
	for _, p := range []*ingestv1.Progress{
		{Phase: ingestv1.Phase_PHASE_FETCH, Current: 1, Total: 4},
		{Phase: ingestv1.Phase_PHASE_INDEX, Current: 4, Total: 4},
	} {
		if err := stream.Send(&ingestv1.IngestResponse{
			Message: &ingestv1.IngestResponse_Progress{Progress: p},
		}); err != nil {
			return err
		}
	}
	return stream.Send(&ingestv1.IngestResponse{
		Message: &ingestv1.IngestResponse_Result{Result: &ingestv1.Result{
			DocId:      "guide",
			Title:      "Guide",
			ChunkCount: 36,
			Outcome:    ingestv1.Outcome_OUTCOME_INGESTED,
			Quality:    typev1.Quality_QUALITY_OK,
			Findings:   []string{"2 chunks carry no heading path"},
		}},
	})
}

// stubJobs answers with one queued job.
type stubJobs struct{}

func (*stubJobs) Enqueue(
	context.Context, *connect.Request[ingestv1.EnqueueRequest],
) (*connect.Response[ingestv1.EnqueueResponse], error) {
	return connect.NewResponse(&ingestv1.EnqueueResponse{
		Jobs: []*ingestv1.QueuedJob{
			{Source: "/library/a.md", JobId: 1, QueuePosition: 1},
			{Source: "/library/b.pdf", JobId: 2, QueuePosition: 2},
		},
	}), nil
}

func (*stubJobs) ListJobs(
	context.Context, *connect.Request[ingestv1.ListJobsRequest],
) (*connect.Response[ingestv1.ListJobsResponse], error) {
	return connect.NewResponse(&ingestv1.ListJobsResponse{
		Jobs: []*ingestv1.Job{{JobId: 7, Source: "/library/guide.md", Status: "queued"}},
	}), nil
}

func (*stubJobs) CancelJob(
	context.Context, *connect.Request[ingestv1.CancelJobRequest],
) (*connect.Response[ingestv1.CancelJobResponse], error) {
	return connect.NewResponse(&ingestv1.CancelJobResponse{Status: "cancelling"}), nil
}
