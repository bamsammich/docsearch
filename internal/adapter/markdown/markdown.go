// Package markdown reads a Markdown file, taking its structure from ATX
// headings. A heading inside a fenced code block is code, not structure.
// Ported from python/docsearch/adapters/markdown.py.
package markdown

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
)

// Python's `\s` is Unicode whitespace and Go's is ASCII, so both patterns
// spell the class out.
var (
	atxHeading = regexp.MustCompile(
		`^(#{1,6})[` + pystr.SpaceChars + `]+(.*?)[` + pystr.SpaceChars + `]*#*$`)
	codeFence = regexp.MustCompile("^[" + pystr.SpaceChars + "]*(```|~~~)")
)

// Extract reads the Markdown file at path. Its title is the first
// top-level heading any block sits under, else the file name.
func Extract(path string) (*domain.Extraction, error) {
	raw, err := pystr.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := reader{blocks: []domain.Block{}}
	for _, line := range pystr.SplitLinesKeepEnds(raw) {
		r.line(line)
	}
	r.flush()

	title := ""
	for _, b := range r.blocks {
		if len(b.HeadingPath) > 0 {
			title = b.HeadingPath[0]
			break
		}
	}
	if title == "" {
		title = pystr.Stem(filepath.Base(path))
	}
	return domain.NewExtraction(title, "markdown", "atx_headings", r.blocks), nil
}

// reader gathers paragraphs line by line. Offsets count code points from the
// start of the file.
type reader struct {
	stack      domain.HeadingStack
	blocks     []domain.Block
	para       []string
	paraOffset int
	offset     int
	inFence    bool
}

func (r *reader) line(line string) {
	width := utf8.RuneCountInString(line)
	defer func() { r.offset += width }()

	stripped := strings.TrimRight(line, "\n")
	if codeFence.MatchString(stripped) {
		r.inFence = !r.inFence
		r.para = append(r.para, stripped)
		return
	}
	var heading []string
	if !r.inFence {
		heading = atxHeading.FindStringSubmatch(stripped)
	}
	switch {
	case heading != nil:
		r.flush()
		r.stack.Push(len(heading[1]), pystr.Strip(heading[2]))
	case pystr.Strip(stripped) == "":
		r.flush()
		r.paraOffset = r.offset + width
	default:
		if len(r.para) == 0 {
			r.paraOffset = r.offset
		}
		r.para = append(r.para, stripped)
	}
}

func (r *reader) flush() {
	if body := pystr.Strip(strings.Join(r.para, "\n")); body != "" {
		r.blocks = append(r.blocks, domain.NewOffsetBlock(r.stack.Path(), r.paraOffset, body))
	}
	r.para = nil
}
