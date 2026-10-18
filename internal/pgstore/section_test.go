package pgstore

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// SectionSuite is the precision suite that guards SectionCovers, which exists
// twice because the rule does: once in Go and once as the SQL in
// ChunksInSection.
//
// Every assertion here is about what must NOT match. A coverage metric cannot
// police this: over-matching increases the number of resolved joins, so it
// moves the reassuring way while results get worse.
type SectionSuite struct{ suite.Suite }

func TestSection(t *testing.T) { suite.Run(t, new(SectionSuite)) }

var fixture = []string{"4", "4.1", "4.2", "4.10", "41", "41.11", "43", "43.6", "43.6.1", "5"}

func covered(ref string) []string {
	var out []string
	for _, section := range fixture {
		if SectionCovers(ref, section) {
			out = append(out, section)
		}
	}
	return out
}

func (s *SectionSuite) TestChapterReferenceCoversOnlyItsOwnSubtree() {
	s.Equal([]string{"4", "4.1", "4.2", "4.10"}, covered("4"))
}

// The match must be component-wise, not a string prefix.
func (s *SectionSuite) TestChapterReferenceDoesNotReachNumericallySimilarChapters() {
	got := covered("4")
	for _, forbidden := range []string{"41", "41.11", "43", "43.6", "43.6.1"} {
		s.NotContains(got, forbidden)
	}
}

func (s *SectionSuite) TestDeepReferenceIsExact() {
	s.Equal([]string{"43.6", "43.6.1"}, covered("43.6"))
	s.Equal([]string{"43.6.1"}, covered("43.6.1"))
}

func (s *SectionSuite) TestTwoDigitChapterDoesNotAbsorbItsOwnPrefix() {
	s.Equal([]string{"41", "41.11"}, covered("41"))
}

func (s *SectionSuite) TestLeafReferenceResolvesToItselfOnly() {
	s.Equal([]string{"5"}, covered("5"))
	s.Equal([]string{"4.1"}, covered("4.1"))
}

func (s *SectionSuite) TestEmptyReferenceCoversNothing() {
	s.Empty(covered(""))
}
