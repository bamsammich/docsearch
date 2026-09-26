// Package docx reads a Word document, taking its structure from paragraph
// heading styles. The rules are python-docx's, which the adapter docsearch
// was ported from read through: which paragraphs count, how a run becomes
// text, and how a paragraph's style is resolved.
//
// The package is read with archive/zip and encoding/xml rather than a DOCX
// library. The Go readers available drop run content python-docx keeps
// (hyperlink text, carriage returns, positional tabs) or carry a license a
// docsearch binary cannot.
package docx

import (
	"archive/zip"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
)

// headingStyle is `^Heading\s+(\d+)$` under re.IGNORECASE. Python's `\d`
// also takes non-ASCII decimal digits; no heading style is named with them.
var headingStyle = regexp.MustCompile(`(?i)^heading[` + pystr.SpaceChars + `]+([0-9]+)\n?$`)

// runChars are the empty run elements that stand for one character.
var runChars = map[string]string{"tab": "\t", "ptab": "\t", "cr": "\n", "noBreakHyphen": "-"}

// paragraph is one body paragraph: its text as python-docx's Paragraph.text
// builds it, and the name of its effective style.
type paragraph struct {
	text  string
	style string
}

// Extract reads the Word document at path. Only paragraphs directly in the
// body count, as python-docx's Document.paragraphs has it: table cells are
// skipped.
func Extract(path string) (*domain.Extraction, error) {
	pkg, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer pkg.Close()

	doc, err := readDocument(&pkg.Reader)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	title, blocks := toBlocks(doc.paragraphs)
	if title == "" {
		title = pystr.Strip(doc.coreTitle)
	}
	if title == "" {
		title = pystr.Stem(filepath.Base(path))
	}
	return domain.NewExtraction(title, "docx", domain.SourceHeadingStyles, blocks), nil
}

// toBlocks turns body paragraphs into blocks. The first paragraph styled
// Title is the title rather than a block; a later one is an ordinary block.
func toBlocks(paragraphs []paragraph) (string, []domain.Block) {
	var stack domain.HeadingStack
	blocks := []domain.Block{}
	offset := 0
	title := ""
	for _, p := range paragraphs {
		text := pystr.Strip(p.text)
		if text == "" {
			continue
		}
		if strings.EqualFold(p.style, "title") && title == "" {
			title = text
			continue
		}
		if level, ok := headingLevel(p.style); ok {
			stack.Push(level, text)
			continue
		}
		blocks = append(blocks, domain.NewOffsetBlock(stack.Path(), offset, text))
		offset += utf8.RuneCountInString(text) + 1
	}
	return title, blocks
}

func headingLevel(style string) (int, bool) {
	m := headingStyle.FindStringSubmatch(style)
	if m == nil {
		return 0, false
	}
	level, err := strconv.Atoi(m[1])
	return level, err == nil
}

// paragraphText concatenates the runs directly in p and in its hyperlinks.
// Runs inside anything else (tracked insertions, content controls, fields)
// are not read, as python-docx does not read them.
func paragraphText(p *xmlNode) string {
	var sb strings.Builder
	for i := range p.Children {
		c := &p.Children[i]
		switch {
		case c.is(nsW, "r"):
			runText(&sb, c)
		case c.is(nsW, "hyperlink"):
			for _, r := range c.children(nsW, "r") {
				runText(&sb, r)
			}
		}
	}
	return sb.String()
}

// runText writes a run's text: w:t as written, tabs as "\t", line breaks
// and carriage returns as "\n", a non-breaking hyphen as "-". A page or
// column break is nothing.
func runText(sb *strings.Builder, r *xmlNode) {
	for i := range r.Children {
		if c := &r.Children[i]; c.XMLName.Space == nsW {
			sb.WriteString(runContentText(c))
		}
	}
}

func runContentText(c *xmlNode) string {
	switch c.XMLName.Local {
	case "t":
		return c.Text
	case "br":
		if t := c.attr(nsW, "type"); t == "" || t == "textWrapping" {
			return "\n"
		}
		return ""
	}
	return runChars[c.XMLName.Local]
}
