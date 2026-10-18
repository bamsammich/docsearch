package pdf

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/bamsammich/docsearch/internal/domain"
)

const (
	// textLayerMinPageShare is the share of pages that must carry
	// extractable text. Below it the document is page images: extraction
	// yields nothing to chunk, and OCR is the prerequisite rather than a
	// tuning problem.
	textLayerMinPageShare = 0.5
	// scriptSamplePages is how many pages the script check reads. Enough to
	// characterise a document without reading an 1,800-page manual twice.
	scriptSamplePages = 40
	// furnitureWarnShare is the share of a document's token mass that may be
	// running headers and footers before it is worth saying so.
	furnitureWarnShare = 0.15
)

// Inspect reports what structure could be derived from a PDF, without
// extracting it.
//
// The questions are the ones that were once answered by hand for one file,
// and the answers are what made that ingest work: is there a text layer, an
// outline, a printed table of contents, a usable font hierarchy.
func Inspect(doc *Document, report *domain.InspectReport) {
	pages := doc.Pages
	count := len(pages)
	report.PageCount = &count

	textLayer(pages, report)
	outline := outlineFinding(doc, report)
	bodySize, headingSizes := analyzeFonts(pages)
	fontHierarchy(bodySize, headingSizes, report)

	boiler := detectBoilerplate(pages, headingSizes)
	if !outline {
		printedContents(pages, boiler, headingSizes, report)
	}
	furniture(pages, boiler, report)
	script(pages, report)
}

// textLayer reports whether there is anything to read at all.
func textLayer(pages []Page, report *domain.InspectReport) {
	withText := 0
	for _, page := range pages {
		if len(page.Lines) > 0 {
			withText++
		}
	}
	if len(pages) > 0 && float64(withText)/float64(len(pages)) < textLayerMinPageShare {
		report.Add(domain.LevelBlocked, "text layer", fmt.Sprintf(
			"only %d of %d pages carry extractable text. This document is page images; "+
				"run it through OCR (ocrmypdf) before ingesting, because extraction has "+
				"nothing to read.", withText, len(pages)))
		return
	}
	report.Add(domain.LevelOK, "text layer", fmt.Sprintf(
		"present on %d of %d pages; no OCR needed", withText, len(pages)))
}

// outlineFinding reports the embedded outline, and whether there was one.
func outlineFinding(doc *Document, report *domain.InspectReport) bool {
	if len(doc.Outline) == 0 {
		report.Add(domain.LevelWarn, "outline",
			"absent. The producer toolchain stripped bookmarks, or none were authored, "+
				"so structure must come from the page content instead.")
		return false
	}
	levels := map[int]bool{}
	for _, entry := range doc.Outline {
		levels[entry.Level] = true
	}
	depths := slices.Sorted(maps.Keys(levels))
	report.PredictedSource = domain.SourceOutline.String()
	report.PredictedTier = domain.TierAuthoritative
	report.Add(domain.LevelOK, "outline", fmt.Sprintf(
		"%d entries, nesting depth %d-%d. Sections, nesting and the page each begins on "+
			"are all declared, so none of the structure has to be inferred.",
		len(doc.Outline), depths[0], depths[len(depths)-1]))
	return true
}

// fontHierarchy reports whether any size stands above the body text.
func fontHierarchy(bodySize float64, headingSizes []float64, report *domain.InspectReport) {
	if len(headingSizes) == 0 {
		report.Add(domain.LevelWarn, "font hierarchy", fmt.Sprintf(
			"no size stands above the %spt body text. Headings in this document are "+
				"styled by weight or colour rather than size, so font detection cannot "+
				"find them and cannot corroborate anything.", points(bodySize)))
		return
	}
	shown := make([]string, 0, min(len(headingSizes), 6))
	for _, size := range headingSizes[:min(len(headingSizes), 6)] {
		shown = append(shown, points(size)+"pt")
	}
	report.Add(domain.LevelOK, "font hierarchy", fmt.Sprintf(
		"body text at %spt; heading candidates at %s",
		points(bodySize), strings.Join(shown, ", ")))
}

// printedContents reports what is left when no outline was authored.
func printedContents(
	pages []Page,
	boiler boilerplate,
	headingSizes []float64,
	report *domain.InspectReport,
) {
	scan := max(40, len(pages)/20)
	entries, _ := reconstructFrontTOC(pages, boiler, scan)
	switch {
	case len(entries) > 0:
		report.PredictedSource = domain.SourceFrontTOC.String()
		report.PredictedTier = domain.TierDeclared
		report.Add(domain.LevelOK, "printed contents", fmt.Sprintf(
			"%d entries reconstructed from the first %d pages. Author-declared, but "+
				"recovered by parsing, so it will be checked against body headings and "+
				"a disagreement fails the ingest.", len(entries), scan))
	case len(headingSizes) > 0:
		report.PredictedSource = domain.SourceFontHeuristic.String()
		report.PredictedTier = domain.TierInferred
		report.Add(domain.LevelWarn, "printed contents",
			"none reconstructable. Structure will be inferred from font sizes alone, "+
				"with nothing to validate it against.")
	default:
		report.PredictedSource = "none"
		report.Add(domain.LevelBlocked, "structure",
			"no outline, no printed contents, and no font hierarchy. Ingest will refuse "+
				"rather than cut the text into fixed windows.")
	}
}

// furniture reports how much of the document's text is a running header or
// footer, which is stripped before chunking.
func furniture(pages []Page, boiler boilerplate, report *domain.InspectReport) {
	var boilerTokens, total int
	for _, page := range pages {
		for _, line := range page.Lines {
			tokens := domain.EstimateTokens(line.Text)
			total += tokens
			if boiler.matches(line.Text) {
				boilerTokens += tokens
			}
		}
	}
	if total == 0 {
		return
	}
	share := float64(boilerTokens) / float64(total)
	level := domain.LevelOK
	if share > furnitureWarnShare {
		level = domain.LevelWarn
	}
	report.Add(level, "page furniture", fmt.Sprintf(
		"%d repeated line(s), %s of the document's token mass. Stripped before chunking; "+
			"a high share means a large part of the raw text is a running header or footer.",
		len(boiler), percent(share)))
}

// script reports letters the token estimator cannot size.
func script(pages []Page, report *domain.InspectReport) {
	var sample []string
	for _, page := range pages[:min(len(pages), scriptSamplePages)] {
		for _, line := range page.Lines {
			sample = append(sample, line.Text)
		}
	}
	share := domain.UncalibratedLetterShare(strings.Join(sample, "\n"))
	if share < domain.UncalibratedScriptNoteShare {
		return
	}
	report.Add(domain.LevelWarn, "script", fmt.Sprintf(
		"%s of letters are in a script the token estimator has no calibration for, so "+
			"chunk sizes will be in an unknown unit and the document may be chunked "+
			"coarser than intended.", percent(share)))
}

// points renders a font size the way Python prints a float, which keeps the
// trailing ".0" that Go's %g drops.
func points(size float64) string {
	shown := strconv.FormatFloat(size, 'g', -1, 64)
	if !strings.ContainsAny(shown, ".e") {
		shown += ".0"
	}
	return shown
}

// percent renders a share as Python's "{:.0%}" does.
func percent(share float64) string {
	return fmt.Sprintf("%.0f%%", share*100)
}
