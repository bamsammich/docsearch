package domain_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

// HeadingSuite checks HeadingStack against the Python adapters' list
// manipulation it replaces.
type HeadingSuite struct{ suite.Suite }

func TestHeading(t *testing.T) { suite.Run(t, new(HeadingSuite)) }

// push is one heading pushed at a level.
type push struct {
	heading string
	level   int
}

// headingCase pushes headings in order and expects the final path.
type headingCase struct {
	name   string
	pushes []push
	want   []string
}

func (s *HeadingSuite) TestPushMatchesPython() {
	tests := []headingCase{
		{name: "nothing pushed", want: []string{}},
		{
			name:   "nested",
			pushes: []push{{level: 1, heading: "A"}, {level: 2, heading: "B"}},
			want:   []string{"A", "B"},
		},
		{
			name: "a sibling replaces its peer and everything under it",
			pushes: []push{
				{level: 1, heading: "A"},
				{level: 2, heading: "B"},
				{level: 3, heading: "C"},
				{level: 2, heading: "D"},
			},
			want: []string{"A", "D"},
		},
		{
			name:   "a skipped level is kept as empty",
			pushes: []push{{level: 1, heading: "A"}, {level: 4, heading: "D"}},
			want:   []string{"A", "", "", "D"},
		},
		{
			name: "level 0 replaces the innermost heading",
			pushes: []push{
				{level: 1, heading: "A"},
				{level: 2, heading: "B"},
				{level: 0, heading: "Z"},
			},
			want: []string{"A", "Z"},
		},
		{
			name:   "level 0 on an empty stack",
			pushes: []push{{level: 0, heading: "Z"}},
			want:   []string{"Z"},
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			var stack domain.HeadingStack
			for _, p := range tt.pushes {
				stack.Push(p.level, p.heading)
			}
			s.Equal(tt.want, stack.Path())
		})
	}
}

func (s *HeadingSuite) TestPathIsACopy() {
	var stack domain.HeadingStack
	stack.Push(1, "A")
	path := stack.Path()
	path[0] = "changed"
	s.Equal([]string{"A"}, stack.Path())
}

func (s *HeadingSuite) TestNewOffsetBlockCopiesTheHeadingPath() {
	path := []string{"A"}
	block := domain.NewOffsetBlock(path, 7, "text")
	path[0] = "changed"
	s.Equal([]string{"A"}, block.HeadingPath)
	s.Equal(map[string]int{"offset": 7}, block.Locator)
	s.Equal([]string{}, domain.NewOffsetBlock(nil, 0, "text").HeadingPath)
}
