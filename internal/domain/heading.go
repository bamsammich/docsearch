package domain

// HeadingStack is the heading ancestry of a document read in order: every
// heading above the current position, outermost first.
type HeadingStack struct {
	path []string
}

// Push puts heading at level (1 for the outermost) and drops everything at
// that level and deeper. A skipped level is kept as "", so a level 4 under
// a level 2 has a path of four entries.
//
// The drop is Python's `del stack[level - 1:]`, negative index included: a
// DOCX style named "Heading 0" replaces the innermost heading.
func (s *HeadingStack) Push(level int, heading string) {
	cut := level - 1
	if cut < 0 {
		cut = max(len(s.path)+cut, 0)
	}
	if cut < len(s.path) {
		s.path = s.path[:cut]
	}
	for len(s.path) < level-1 {
		s.path = append(s.path, "")
	}
	s.path = append(s.path, heading)
}

// Path is a copy of the current ancestry, empty rather than nil.
func (s *HeadingStack) Path() []string {
	return append([]string{}, s.path...)
}

// NewOffsetBlock is a block located by its character offset in the document,
// for formats without pages.
func NewOffsetBlock(headingPath []string, offset int, text string) Block {
	return Block{
		Locator:     map[string]int{"offset": offset},
		Text:        text,
		HeadingPath: append([]string{}, headingPath...),
	}
}

// NewExtraction is an extraction of a format without pages. Pages and index
// terms are empty rather than nil, matching Python's, so the two serialize
// alike.
func NewExtraction(title, format, structureSource string, blocks []Block) *Extraction {
	return &Extraction{
		Pages:       map[int]string{},
		Diagnostics: map[string]any{"structure_source": structureSource},
		Title:       title,
		Format:      format,
		Blocks:      blocks,
		IndexTerms:  [][2]string{},
	}
}
