package domain_test

import (
	"github.com/bamsammich/docsearch/internal/domain"
)

func (s *StructureSuite) TestAReportWithoutAJudgePersistsNoJudgeFields() {
	got := s.persisted(healthy())
	for _, key := range []string{"judge", "judge_unused", "judged_not_headings"} {
		s.NotContains(got, key)
	}
}

func (s *StructureSuite) TestWhatAJudgeTookOutReachesTheCaller() {
	r := domain.NewStructureReport(map[string]any{
		"structure_source": "font_heuristic",
		"heading_judge": map[string]any{
			"judge": "bridge/1", "asked": 6, "vetoed": []any{"p2:l2 9. Tap Save"},
		},
	})
	s.Equal([]string{"p2:l2 9. Tap Save"}, r.JudgedNotHeadings)
	raw, err := r.JSON()
	s.Require().NoError(err)
	_, notes := domain.Summary(string(raw))
	s.Contains(notes, "the heading judge 'bridge/1' ruled that 1 line(s) the type sizes "+
		"took for headings are not, and they are read as body text: p2:l2 9. Tap Save")
}

func (s *StructureSuite) TestAJudgeThatWasNotUsedSaysWhy() {
	r := domain.NewStructureReport(map[string]any{
		"structure_source": "font_heuristic",
		"heading_judge": map[string]any{
			"asked": 6, "vetoed": []any{}, "unused": "the judge could not be asked: timeout",
		},
	})
	s.Contains(r.Notes(),
		"headings come from the rules alone, because the judge could not be asked: timeout")
}
