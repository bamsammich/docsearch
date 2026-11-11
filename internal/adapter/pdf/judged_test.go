package pdf_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/judge"
)

// JudgedSuite checks what a heading judge may change about a build: it can
// take a candidate out, and nothing else.
type JudgedSuite struct{ suite.Suite }

func TestJudged(t *testing.T) { suite.Run(t, new(JudgedSuite)) }

// fakeJudge scores every candidate whose text contains a key of low as that
// key's value, and everything else as a heading.
type fakeJudge struct {
	err   error
	low   map[string]float64
	asked []judge.Heading
}

func (f *fakeJudge) Headings(
	_ context.Context,
	_ string,
	candidates []judge.Heading,
) (judge.Verdicts, error) {
	f.asked = append(f.asked, candidates...)
	if f.err != nil {
		return judge.Verdicts{}, f.err
	}
	out := judge.Verdicts{Judge: "fake/1", Heading: map[string]float64{}}
	for _, c := range candidates {
		out.Heading[c.ID] = 0.95
		for text, p := range f.low {
			if strings.Contains(c.Text, text) {
				out.Heading[c.ID] = p
			}
		}
	}
	return out, nil
}

// strayStep is five numbered sections, with a procedure step set at the
// heading size on the second page. Its "9." advances the numbering past
// every real section after it, so the ordering rule refuses sections 3 to 5.
func strayStep() *pdf.Document {
	var pages []pdf.Page
	for i := range 5 {
		n := strconv.Itoa(i + 1)
		lines := []pdf.Line{
			line(n+". Topic "+n, 50, 60, 16),
			line("Body text of topic "+n+".", 50, 90, 10),
		}
		if i == 1 {
			lines = append(lines,
				line("9. Tap Save", 50, 120, 16),
				line("The settings are kept.", 50, 150, 10))
		}
		pages = append(pages, pdf.Page{Lines: lines})
	}
	return &pdf.Document{Pages: pages}
}

func headings(x *domain.Extraction) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, b := range x.Blocks {
		path := strings.Join(b.HeadingPath, " > ")
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

func (s *JudgedSuite) build(j pdf.HeadingJudge, doc *pdf.Document) *domain.Extraction {
	got, err := pdf.BuildJudged(s.T().Context(), doc, "manual.pdf", j)
	s.Require().NoError(err)
	return got
}

func (s *JudgedSuite) TestTheRulesAloneLetAStrayStepScatterTheBook() {
	got, err := pdf.Build(strayStep(), "manual.pdf")
	s.Require().NoError(err)
	s.Contains(headings(got), "9. Tap Save")
	s.NotContains(headings(got), "3. Topic 3")
}

func (s *JudgedSuite) TestAVetoedStepIsBodyTextAndFreesTheNumbering() {
	j := &fakeJudge{low: map[string]float64{"Tap Save": 0.05}}
	got := s.build(j, strayStep())
	s.Equal([]string{
		"1. Topic 1", "2. Topic 2", "3. Topic 3", "4. Topic 4", "5. Topic 5",
	}, headings(got))
	s.Contains(bodyText(got.Blocks)["2. Topic 2"], "9. Tap Save")
	s.Equal(map[string]any{
		"judge": "fake/1", "asked": 6, "vetoed": []string{"p2:l2 9. Tap Save"},
	}, got.Diagnostics["heading_judge"])
	report := domain.NewStructureReport(got.Diagnostics)
	s.Equal("fake/1", report.Judge)
	s.Equal([]string{"p2:l2 9. Tap Save"}, report.JudgedNotHeadings)
}

func (s *JudgedSuite) TestADoubtfulVerdictLeavesTheCandidateWithTheRules() {
	j := &fakeJudge{low: map[string]float64{"Tap Save": pdf.VetoBelow}}
	got := s.build(j, strayStep())
	s.Contains(headings(got), "9. Tap Save")
}

func (s *JudgedSuite) TestAJudgeThatFailsLeavesTheRulesResult() {
	want, err := pdf.Build(strayStep(), "manual.pdf")
	s.Require().NoError(err)
	got := s.build(&fakeJudge{err: errors.New("connection refused")}, strayStep())
	s.Equal(want.Blocks, got.Blocks)
	report := domain.NewStructureReport(got.Diagnostics)
	s.Equal("the judge could not be asked: connection refused", report.JudgeUnused)
}

func (s *JudgedSuite) TestAJudgeThatRulesOutEveryHeadingIsIgnored() {
	want, err := pdf.Build(strayStep(), "manual.pdf")
	s.Require().NoError(err)
	got := s.build(&fakeJudge{low: map[string]float64{".": 0}}, strayStep())
	s.Equal(want.Blocks, got.Blocks)
	report := domain.NewStructureReport(got.Diagnostics)
	s.Contains(report.JudgeUnused, "every candidate")
	s.Empty(report.JudgedNotHeadings)
}

func (s *JudgedSuite) TestAJudgeIsShownTheLinesAroundACandidate() {
	j := &fakeJudge{}
	s.build(j, strayStep())
	var step judge.Heading
	for _, c := range j.asked {
		if c.Text == "9. Tap Save" {
			step = c
		}
	}
	s.Equal(judge.Heading{
		ID: "p2:l2", Text: "9. Tap Save", Page: 2,
		Before: "Body text of topic 2.", After: "The settings are kept.",
	}, step)
}

func (s *JudgedSuite) TestAnOutlinedDocumentAsksNothing() {
	doc := strayStep()
	doc.Outline = []pdf.OutlineEntry{{Title: "1. Topic 1", Level: 1, Page: 1}}
	j := &fakeJudge{}
	got := s.build(j, doc)
	s.Empty(j.asked)
	s.NotContains(got.Diagnostics, "heading_judge")
}

func (s *JudgedSuite) TestAVetoedTitleIsBodyText() {
	got := s.build(&fakeJudge{low: map[string]float64{"FIGURE": 0}},
		&pdf.Document{Pages: []pdf.Page{
			chapter("PARTS OF THE UNIT"),
			chapter("FIGURE 2 THE UNIT"),
			chapter("ACCESSORIES"),
			chapter("CLEANING THE HEAD"),
			chapter("TROUBLESHOOTING"),
		}})
	s.Equal([]string{
		"PARTS OF THE UNIT", "ACCESSORIES", "CLEANING THE HEAD", "TROUBLESHOOTING",
	}, headings(got))
	s.Contains(bodyText(got.Blocks)["PARTS OF THE UNIT"], "FIGURE 2 THE UNIT")
}
