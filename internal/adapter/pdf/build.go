package pdf

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pystr"
)

// ErrNoStructure is returned for a PDF with no outline, no printed contents
// and no font hierarchy: nothing to cut chunks at.
var ErrNoStructure = errors.New("no usable structure")

// Structure sources, as the "structure_source" diagnostic names them.
const (
	sourceOutline  = "outline"
	sourceFrontTOC = "front_toc"
	sourceFonts    = "font_heuristic"
)

// figureCaptionMaxTokens is the size at or below which a block on a page
// with figures is a figure callout: kept with its section, never a chunk of
// its own or a split point.
const figureCaptionMaxTokens = 25

// Build turns a PDF's primitives into an extraction. name is the file name,
// for the title fallback and error messages.
func Build(doc *Document, name string) (*domain.Extraction, error) {
	pages := doc.Pages
	figureTops, figures := filterFigures(pages)
	// Fonts first: boilerplate detection needs the heading sizes to leave
	// them alone.
	bodySize, headingSizes := analyzeFonts(pages)
	boiler := detectBoilerplate(pages, headingSizes)
	diagnostics := map[string]any{
		"body_font_size":             bodySize,
		"heading_font_sizes":         headingSizes,
		"boilerplate_lines_stripped": len(boiler),
		"figures":                    figures.diagnostic(),
	}

	entries, source, tocPageMax := structureSource(doc, boiler)
	if source == "" && len(headingSizes) > 0 {
		source = sourceFonts
	}
	if source == "" {
		return nil, fmt.Errorf("%w: %s: no outline tree, no printed table of contents, "+
			"and no usable font hierarchy -- cannot derive chunk boundaries. Refusing to "+
			"fall back to fixed token windows", ErrNoStructure, name)
	}
	diagnostics["structure_source"] = source
	diagnostics["toc_entries"] = len(entries)
	titles := map[string]string{}
	for _, e := range entries {
		titles[e.section] = e.title
	}

	var blocks []domain.Block
	indexTerms := [][2]string{}
	if source == sourceOutline {
		blocks = outlineBlocks(pages, entries, titles, boiler, figureTops, diagnostics)
	} else {
		n := numbered{
			pages: pages, entries: entries, titles: titles, headingSizes: headingSizes,
			boiler: boiler, figureTops: figureTops, tocPageMax: tocPageMax,
		}
		blocks, indexTerms = n.blocks(diagnostics)
	}
	markFigureCallouts(blocks)

	pageText := make(map[int]string, len(pages))
	for i, p := range pages {
		pageText[i+1] = p.Text
	}
	return &domain.Extraction{
		Pages:       pageText,
		Diagnostics: diagnostics,
		PageCount:   ptr(len(pages)),
		Title:       title(doc.Title, name),
		Format:      "pdf",
		Blocks:      blocks,
		IndexTerms:  indexTerms,
	}, nil
}

// structureSource picks the outline when there is one, else a printed
// contents listing among the first pages. It returns "" as the source when
// neither exists, and the last printed contents page, -1 when there is none.
func structureSource(doc *Document, boiler boilerplate) ([]tocEntry, string, int) {
	if len(doc.Outline) > 0 {
		return outlineEntries(doc.Outline), sourceOutline, -1
	}
	scan := max(40, len(doc.Pages)/20)
	entries, tocPages := reconstructFrontTOC(doc.Pages, boiler, scan)
	if len(entries) > 0 {
		return entries, sourceFrontTOC, slices.Max(slices.Collect(maps.Keys(tocPages)))
	}
	return []tocEntry{}, "", -1
}

// title is the metadata title, else the file name less its suffix.
func title(meta, name string) string {
	stem := pystr.Stem(name)
	t := meta
	if t == "" {
		t = stem
	}
	if t = pystr.Strip(t); t == "" {
		return stem
	}
	return t
}

// outlineBlocks places the outline's entries on their pages and emits
// blocks from them. An outline carries positions as well as names, so it
// is not checked against headings found by font: requiring that refuses
// documents whose headings differ by weight or colour rather than size.
func outlineBlocks(pages []Page, entries []tocEntry, titles map[string]string,
	boiler boilerplate, figureTops [][]float64, diagnostics map[string]any,
) []domain.Block {
	placements, located := locateOutlineHeadings(pages, entries, boiler)
	contents := contentsPages(pages, entries, boiler)
	diagnostics["outline_placement"] = map[string]any{
		"entries":                len(entries),
		"located_by_title":       located,
		"placed_at_page_top":     len(placements) - located,
		"contents_pages_skipped": len(contents),
	}
	diagnostics["printed_page_offset"] = 0
	return emitOutlineBlocks(pages, placements, titles, boiler, figureTops, contents)
}

// markFigureCallouts marks short blocks on pages with figures: a callout
// never stands alone.
func markFigureCallouts(blocks []domain.Block) {
	for i := range blocks {
		b := &blocks[i]
		if b.ImageCount > 0 && domain.EstimateTokens(b.Text) <= figureCaptionMaxTokens {
			b.FigureOnly = true
		}
	}
}
