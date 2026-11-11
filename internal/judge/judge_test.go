package judge_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/judge"
)

type JudgeSuite struct{ suite.Suite }

func TestJudge(t *testing.T) { suite.Run(t, new(JudgeSuite)) }

// request is what a judge receives.
type request struct {
	Document   string          `json:"document"`
	Candidates []judge.Heading `json:"candidates"`
}

// serve starts a judge that answers each request with answer.
func (s *JudgeSuite) serve(answer func(request) any) (*judge.Client, *[]request) {
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Equal(judge.HeadingsPath, r.URL.Path)
		s.Equal("Bearer secret", r.Header.Get("Authorization"))
		var req request
		s.NoError(json.NewDecoder(r.Body).Decode(&req))
		got = append(got, req)
		s.NoError(json.NewEncoder(w).Encode(answer(req)))
	}))
	s.T().Cleanup(srv.Close)
	client, err := judge.New(srv.URL+"/", "secret")
	s.Require().NoError(err)
	return client, &got
}

func candidates(n int) []judge.Heading {
	out := make([]judge.Heading, n)
	for i := range out {
		out[i] = judge.Heading{
			ID:   "c" + strconv.Itoa(i),
			Text: "Heading " + strconv.Itoa(i),
			Page: 1,
		}
	}
	return out
}

func (s *JudgeSuite) TestAnswersAreKeyedByCandidate() {
	client, got := s.serve(func(r request) any {
		return map[string]any{"judge": "test/1", "headings": []map[string]any{
			{"id": r.Candidates[0].ID, "heading": 0.9},
			{"id": r.Candidates[1].ID, "heading": 0.1},
		}}
	})
	verdicts, err := client.Headings(s.T().Context(), "manual.pdf", candidates(2))
	s.Require().NoError(err)
	s.Equal("test/1", verdicts.Judge)
	s.Equal(map[string]float64{"c0": 0.9, "c1": 0.1}, verdicts.Heading)
	s.Equal("manual.pdf", (*got)[0].Document)
}

func (s *JudgeSuite) TestALongDocumentIsAskedInBatches() {
	client, got := s.serve(func(r request) any {
		out := []map[string]any{}
		for _, c := range r.Candidates {
			out = append(out, map[string]any{"id": c.ID, "heading": 1})
		}
		return map[string]any{"judge": "test/1", "headings": out}
	})
	verdicts, err := client.Headings(s.T().Context(), "manual.pdf", candidates(judge.Batch+1))
	s.Require().NoError(err)
	s.Len(*got, 2)
	s.Len(verdicts.Heading, judge.Batch+1)
}

func (s *JudgeSuite) TestAnAnswerOutsideTheContractFailsTheWholeCall() {
	for name, verdict := range map[string]map[string]any{
		"unknown candidate": {"id": "nobody", "heading": 0.5},
		"no probability":    {"id": "c0"},
		"above one":         {"id": "c0", "heading": 1.5},
		"below zero":        {"id": "c0", "heading": -0.1},
	} {
		s.Run(name, func() {
			client, _ := s.serve(func(request) any {
				return map[string]any{"headings": []map[string]any{verdict}}
			})
			_, err := client.Headings(s.T().Context(), "manual.pdf", candidates(1))
			s.ErrorIs(err, judge.ErrBadAnswer)
		})
	}
}

func (s *JudgeSuite) TestAFailingJudgeIsAnError() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	s.T().Cleanup(srv.Close)
	client, err := judge.New(srv.URL, "")
	s.Require().NoError(err)
	_, err = client.Headings(s.T().Context(), "manual.pdf", candidates(1))
	s.ErrorContains(err, "503")
}

func (s *JudgeSuite) TestOnlyAnHTTPURLNamesAJudge() {
	for _, bad := range []string{"", "judge.local", "ftp://judge.local", "http://"} {
		_, err := judge.New(bad, "")
		s.Errorf(err, "%q", bad)
	}
}
