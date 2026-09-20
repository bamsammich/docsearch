// Package text reads a plain-text file, which has no structure: every run of
// text between blank lines is one block. Ported from
// python/docsearch/adapters/text.py.
package text

import (
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
)

// paragraphBreak separates blocks. Offsets count it as two characters.
const paragraphBreak = "\n\n"

// Extract reads the plain-text file at path.
func Extract(path string) (*domain.Extraction, error) {
	raw, err := pystr.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blocks := []domain.Block{}
	offset := 0
	for _, para := range strings.Split(raw, paragraphBreak) {
		if body := pystr.Strip(para); body != "" {
			blocks = append(blocks, domain.NewOffsetBlock(nil, offset, body))
		}
		offset += utf8.RuneCountInString(para) + len(paragraphBreak)
	}
	title := pystr.Stem(filepath.Base(path))
	return domain.NewExtraction(title, "text", domain.SourceBlankLines, blocks), nil
}
