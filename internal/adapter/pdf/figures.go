package pdf

import (
	"cmp"
	"slices"
)

const (
	// figureFurniturePageFraction is the share of pages above which an
	// image placed on them is page furniture, a corner logo, not a figure.
	figureFurniturePageFraction = 0.5
	// minFigureArea is the smallest placed area, in square points, that
	// counts as a figure; below it is an inline icon or a bullet glyph.
	// 400 is roughly 20 by 20.
	minFigureArea = 400.0
)

// figureStats counts what filterFigures kept and dropped.
type figureStats struct {
	furnitureIDs       []int64
	distinctIDs        int
	placementsTotal    int
	droppedAsFurniture int
	droppedAsTooSmall  int
	droppedAsDuplicate int
	figuresKept        int
	pagesWithNoFigure  int
}

// diagnostic is the "figures" diagnostic, keyed as the Python adapter keys it.
func (s figureStats) diagnostic() map[string]any {
	return map[string]any{
		"distinct_xrefs":               s.distinctIDs,
		"furniture_xrefs":              s.furnitureIDs,
		"placements_total":             s.placementsTotal,
		"dropped_as_furniture":         s.droppedAsFurniture,
		"dropped_as_too_small":         s.droppedAsTooSmall,
		"dropped_as_duplicate_on_page": s.droppedAsDuplicate,
		"figures_kept":                 s.figuresKept,
		"pages_with_no_figure":         s.pagesWithNoFigure,
	}
}

// filterFigures reduces each page's image placements to its figures, and
// returns the top of each kept figure, per page.
//
// A raw image count is not a figure count. A logo drawn on every page is a
// placement like any other, and counting it makes image_count a constant
// rather than a signal. Three filters: an image on most pages is furniture,
// a tiny placement is an icon or bullet, and one image placed twice on a
// page is one figure.
func filterFigures(pages []Page) ([][]float64, figureStats) {
	onPages := imagePages(pages)
	furniture, stats := furnitureOf(onPages, len(pages))
	for _, p := range pages {
		stats.placementsTotal += len(p.Images)
	}
	kept := make([][]float64, len(pages))
	for pno, p := range pages {
		kept[pno] = keepFigures(sortedImages(p.Images), furniture, &stats)
		stats.figuresKept += len(kept[pno])
		if len(kept[pno]) == 0 {
			stats.pagesWithNoFigure++
		}
	}
	return kept, stats
}

// imagePages maps each image to the pages it is placed on.
func imagePages(pages []Page) map[int64]map[int]bool {
	onPages := map[int64]map[int]bool{}
	for pno, p := range pages {
		for _, im := range p.Images {
			if onPages[im.ID] == nil {
				onPages[im.ID] = map[int]bool{}
			}
			onPages[im.ID][pno] = true
		}
	}
	return onPages
}

// furnitureOf picks the images placed on most pages, and starts the stats
// with them.
func furnitureOf(onPages map[int64]map[int]bool, pageCount int) (map[int64]bool, figureStats) {
	furniture := map[int64]bool{}
	stats := figureStats{distinctIDs: len(onPages), furnitureIDs: []int64{}}
	for id, seen := range onPages {
		if float64(len(seen)) > float64(pageCount)*figureFurniturePageFraction {
			furniture[id] = true
			stats.furnitureIDs = append(stats.furnitureIDs, id)
		}
	}
	slices.Sort(stats.furnitureIDs)
	return furniture, stats
}

func keepFigures(images []Image, furniture map[int64]bool, stats *figureStats) []float64 {
	seen := map[int64]bool{}
	var tops []float64
	for _, im := range images {
		switch {
		case furniture[im.ID]:
			stats.droppedAsFurniture++
		case im.Area < minFigureArea:
			stats.droppedAsTooSmall++
		case seen[im.ID]:
			stats.droppedAsDuplicate++
		default:
			seen[im.ID] = true
			tops = append(tops, im.Y0)
		}
	}
	return tops
}

// sortedImages orders placements by top, then identity, then area, as the
// Python adapter sorts its (y, xref, area) tuples.
func sortedImages(images []Image) []Image {
	out := slices.Clone(images)
	slices.SortStableFunc(out, func(a, b Image) int {
		return cmp.Or(cmpFloat(a.Y0, b.Y0), cmp.Compare(a.ID, b.ID), cmpFloat(a.Area, b.Area))
	})
	return out
}
