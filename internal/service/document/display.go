package document

import (
	"fmt"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/bamsammich/docsearch/internal/domain"
)

const (
	// detailWidth is where a finding's explanation wraps.
	detailWidth = 76
	// pathWidth is where a heading path is cut, so that one chunk stays on
	// one line whatever it was called.
	pathWidth = 88
	// pageListLimit is how many uncovered pages are named before the rest
	// are counted instead.
	pageListLimit = 12
)

// verdictSummary says what a verdict means for whoever reads a result.
var verdictSummary = map[domain.Verdict]string{
	domain.VerdictGood:     "chunking looks healthy",
	domain.VerdictDegraded: "searchable, with findings that cap retrieval quality",
	domain.VerdictUnusable: "structure extraction effectively failed",
}

// thousands groups digits the way the Python report does.
var thousands = message.NewPrinter(language.English)

// Display is the report as `docsearch verify` prints it.
//
// One divergence from Python, which printed the literal "None" for a
// document with no pages: a Markdown file reads as "-" here, the same dash
// `docsearch list` prints for a count it does not have.
func (r *VerifyReport) Display() string {
	var b strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	r.header(line)
	line("")
	line("10 longest chunks:")
	for _, c := range r.Measurements.Longest {
		line("%s", chunkLine(c))
	}
	line("")
	line("10 shortest chunks:")
	for _, c := range r.Measurements.Shortest {
		line("%s", chunkLine(c))
	}

	line("")
	r.grade(line)

	line("")
	if len(r.Problems) > 0 {
		line("PROBLEMS:")
		for _, p := range r.Problems {
			line("  - %s", p)
		}
	} else {
		line("No structural problems detected.")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// header is what the document is, and how its chunks came out.
func (r *VerifyReport) header(line func(string, ...any)) {
	line("doc_id      %s", r.Document.DocID)
	line("title       %s", r.Document.Title)
	line("format      %s   status: %s", r.Document.Format, r.Document.Status)
	line("pages       %s", pageCount(r.Document.PageCount))
	line("chunks      %d", r.Measurements.ChunkCount)
	line("tokens      total=%s  mean=%d",
		thousands.Sprintf("%d", r.Measurements.Tokens.Total), r.Measurements.Tokens.Mean)
	line("            min=%d  median=%d  p95=%d  max=%d",
		r.Measurements.Tokens.Min, r.Measurements.Tokens.Median,
		r.Measurements.Tokens.P95, r.Measurements.Tokens.Max)
	line("images      %d chunks reference at least one image", r.Measurements.ChunksWithImages)
	if r.IndexTerms > 0 {
		line("index       %d terms; %d unjoinable", r.IndexTerms, len(r.UnjoinableSections))
	}
	line("gaps        %s", gaps(r.Measurements.UncoveredPages))
}

// grade is the verdict and every finding behind it.
func (r *VerifyReport) grade(line func(string, ...any)) {
	line("VERDICT     %s - %s", r.Verdict, verdictSummary[r.Verdict])
	if r.Measurements.ChunkCount < domain.GradeMinChunks {
		line("            (only %d chunks; below %d the distribution is too small to grade)",
			r.Measurements.ChunkCount, domain.GradeMinChunks)
	}
	for _, f := range r.Findings {
		line("")
		line("  [%s] %s", f.Severity, f.Code)
		for _, wrapped := range wrap(f.Detail, detailWidth) {
			line("      %s", wrapped)
		}
	}
}

// pageCount reads as a dash where a document has no pages to count.
func pageCount(pages *int) string {
	if pages == nil {
		return "-"
	}
	return fmt.Sprint(*pages)
}

// gaps names the pages no chunk covers, or says there are none.
func gaps(pages []int) string {
	if len(pages) == 0 {
		return "none - every page is covered by a chunk"
	}
	shown := pages
	more := ""
	if len(shown) > pageListLimit {
		shown = shown[:pageListLimit]
		more = fmt.Sprintf(" ... (+%d more)", len(pages)-pageListLimit)
	}
	named := make([]string, len(shown))
	for i, p := range shown {
		named[i] = fmt.Sprint(p)
	}
	return fmt.Sprintf("%d pages covered by no chunk: %s%s",
		len(pages), strings.Join(named, ", "), more)
}

// chunkLine is one extreme chunk, named by its ordinal.
func chunkLine(c domain.SizedChunk) string {
	return fmt.Sprintf("  %6d tok  #%-7d %s", c.Tokens, c.Ordinal, cut(c.HeadingPath, pathWidth))
}

// cut shortens a heading path to width characters, counting characters
// rather than bytes so that a path is not cut through one.
func cut(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width])
}

// wrap breaks text at width, greedily, the way the Python report does: on
// whitespace, never inside a word, however long the word is.
func wrap(text string, width int) []string {
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		switch {
		case current == "":
			current = word
		case len(current)+1+len(word) > width:
			lines = append(lines, current)
			current = word
		default:
			current += " " + word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}
