package connectapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/api/connectapi"
	"github.com/bamsammich/docsearch/internal/api/connectapi/mocks"
	documentv1 "github.com/bamsammich/docsearch/internal/api/docsearch/document/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/document/v1/documentv1connect"
	typev1 "github.com/bamsammich/docsearch/internal/api/docsearch/type/v1"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/document"
)

// errNotFound stands in for the repository's sentinel, which is what the
// handler is told to recognise.
var errNotFound = errors.New("no such document")

// DocumentAPISuite drives the real Connect stack over HTTP, because what
// this package is for is the wire.
type DocumentAPISuite struct {
	suite.Suite
	documents *mocks.MockDocuments
	client    documentv1connect.DocumentServiceClient
}

func TestDocumentAPI(t *testing.T) { suite.Run(t, new(DocumentAPISuite)) }

func (s *DocumentAPISuite) SetupTest() {
	s.documents = mocks.NewMockDocuments(s.T())

	path, handler := connectapi.NewDocumentServer(s.documents, errNotFound)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	s.T().Cleanup(server.Close)

	s.client = documentv1connect.NewDocumentServiceClient(server.Client(), server.URL)
}

func (s *DocumentAPISuite) TestListCarriesTheGradeAndTheCounts() {
	pages, chunks := 458, 281
	s.documents.EXPECT().List(mock.Anything).Return([]document.Document{{
		PageCount:  &pages,
		ChunkCount: &chunks,
		DocID:      "manual",
		Title:      "Reference Manual",
		Format:     "pdf",
		Status:     "ready",
		Warnings:   []string{"2 chunk(s) carry no heading path"},
		SourceKind: domain.SourceKindFile,
		Quality:    domain.QualityDegraded,
	}}, nil)

	res, err := s.client.List(s.T().Context(), connect.NewRequest(&documentv1.ListRequest{}))
	s.Require().NoError(err)
	s.Require().Len(res.Msg.GetDocuments(), 1)

	got := res.Msg.GetDocuments()[0]
	s.Equal("manual", got.GetDocId())
	s.Equal(typev1.Quality_QUALITY_DEGRADED, got.GetQuality())
	s.Equal(typev1.SourceKind_SOURCE_KIND_FILE, got.GetSourceKind())
	s.Equal(int64(458), got.GetPageCount())
	s.Equal(int64(281), got.GetChunkCount())
	s.Equal([]string{"2 chunk(s) carry no heading path"}, got.GetWarnings())
}

func (s *DocumentAPISuite) TestADocumentWithNoPagesSendsNoPageCount() {
	// A markdown document has no pages, which is not a page count of zero.
	s.documents.EXPECT().List(mock.Anything).Return([]document.Document{{
		DocID: "guide", Title: "Guide", Format: "markdown", Status: "ready",
	}}, nil)

	res, err := s.client.List(s.T().Context(), connect.NewRequest(&documentv1.ListRequest{}))
	s.Require().NoError(err)
	s.Nil(res.Msg.GetDocuments()[0].PageCount)
}

func (s *DocumentAPISuite) TestVerifyKeepsIntegrityAndQualityApart() {
	s.documents.EXPECT().Verify(mock.Anything, "sliced").Return(&document.VerifyReport{
		Document: document.Document{DocID: "sliced", Status: "ready"},
		Problems: []string{"chunk ordinals skip at [3]"},
		Findings: []domain.Finding{{
			Code: "unaddressable", Detail: "35 chunks share only 5 distinct heading paths",
			Severity: domain.VerdictUnusable,
		}},
		Measurements: domain.Measurements{
			ChunkCount:        35,
			Tokens:            domain.TokenSpread{Min: 900, Median: 1190, Max: 1190, Total: 41650},
			ScatteredSections: []string{"2"},
			OrdinalGaps:       []int{3},
		},
		Verdict: domain.VerdictUnusable,
	}, nil)

	res, err := s.client.Verify(s.T().Context(),
		connect.NewRequest(&documentv1.VerifyRequest{DocId: "sliced"}))
	s.Require().NoError(err)

	report := res.Msg.GetReport()
	s.Equal([]string{"chunk ordinals skip at [3]"}, report.GetProblems())
	s.Require().Len(report.GetFindings(), 1)
	s.Equal("unaddressable", report.GetFindings()[0].GetCode())
	s.Equal(typev1.Verdict_VERDICT_UNUSABLE, report.GetFindings()[0].GetSeverity())
	s.Equal(typev1.Verdict_VERDICT_UNUSABLE, report.GetVerdict())
	s.Equal(int64(35), report.GetMeasurements().GetChunkCount())
	s.Equal(int64(1190), report.GetMeasurements().GetTokens().GetMedian())
	s.Equal([]int64{3}, report.GetMeasurements().GetOrdinalGaps())
	s.Equal([]string{"2"}, report.GetMeasurements().GetScatteredSections())
}

func (s *DocumentAPISuite) TestInspectCarriesEveryFindingAndTheVerdict() {
	pages := 70
	s.documents.EXPECT().Inspect(mock.Anything, "/library/manual.pdf").Return(
		&domain.InspectReport{
			Target:          "/library/manual.pdf",
			Format:          "pdf",
			PredictedSource: "font_heuristic",
			PredictedTier:   domain.TierInferred,
			PageCount:       &pages,
			Findings: []domain.InspectFinding{
				{Label: "text layer", Detail: "present on 70 of 70 pages", Level: domain.LevelOK},
				{Label: "outline", Detail: "absent.", Level: domain.LevelWarn},
			},
		}, nil)

	res, err := s.client.Inspect(s.T().Context(),
		connect.NewRequest(&documentv1.InspectRequest{Target: "/library/manual.pdf"}))
	s.Require().NoError(err)

	report := res.Msg.GetReport()
	s.Equal("font_heuristic", report.GetPredictedSource())
	s.Equal(domain.TierInferred, report.GetPredictedTier())
	s.Equal(int64(70), report.GetPageCount())
	s.False(report.GetBlocked())
	s.Require().Len(report.GetFindings(), 2)
	s.Equal(typev1.Level_LEVEL_OK, report.GetFindings()[0].GetLevel())
	s.Equal(typev1.Level_LEVEL_WARN, report.GetFindings()[1].GetLevel())
}

func (s *DocumentAPISuite) TestABlockedTargetSaysSoWithoutFailing() {
	// Reconnaissance answered the question; the answer is that ingest would
	// refuse the document.
	s.documents.EXPECT().Inspect(mock.Anything, mock.Anything).Return(
		&domain.InspectReport{
			Target: "/library/scanned.pdf",
			Format: "pdf",
			Findings: []domain.InspectFinding{{
				Label: "text layer", Detail: "run it through OCR", Level: domain.LevelBlocked,
			}},
		}, nil)

	res, err := s.client.Inspect(s.T().Context(),
		connect.NewRequest(&documentv1.InspectRequest{Target: "/library/scanned.pdf"}))
	s.Require().NoError(err)
	s.True(res.Msg.GetReport().GetBlocked())
}

func (s *DocumentAPISuite) TestADocumentTheIndexDoesNotHoldIsNotFound() {
	s.documents.EXPECT().Verify(mock.Anything, "absent").Return(nil, errNotFound)

	_, err := s.client.Verify(s.T().Context(),
		connect.NewRequest(&documentv1.VerifyRequest{DocId: "absent"}))
	s.Require().Error(err)
	s.Equal(connect.CodeNotFound, connect.CodeOf(err))
}

func (s *DocumentAPISuite) TestAnEmptyIdentifierIsRefusedBeforeAnythingIsAsked() {
	_, err := s.client.Verify(s.T().Context(),
		connect.NewRequest(&documentv1.VerifyRequest{}))
	s.Require().Error(err)
	s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = s.client.Remove(s.T().Context(),
		connect.NewRequest(&documentv1.RemoveRequest{}))
	s.Require().Error(err)
	s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = s.client.Inspect(s.T().Context(),
		connect.NewRequest(&documentv1.InspectRequest{}))
	s.Require().Error(err)
	s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))
}

func (s *DocumentAPISuite) TestRemovingADocumentReportsNothingBack() {
	s.documents.EXPECT().Remove(mock.Anything, "guide").Return(nil)

	_, err := s.client.Remove(s.T().Context(),
		connect.NewRequest(&documentv1.RemoveRequest{DocId: "guide"}))
	s.Require().NoError(err)
}

func (s *DocumentAPISuite) TestAFailureNobodyAnticipatedStaysInternal() {
	s.documents.EXPECT().List(mock.Anything).Return(nil, errors.New("database is locked"))

	_, err := s.client.List(s.T().Context(), connect.NewRequest(&documentv1.ListRequest{}))
	s.Require().Error(err)
	s.Equal(connect.CodeInternal, connect.CodeOf(err))
}
