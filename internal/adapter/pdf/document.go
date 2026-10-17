// Package pdf reads a PDF into an extraction.
//
// The work splits in two. An Engine reads the primitives a PDF engine
// offers: text lines with a position and a font size, plain page text, image
// placements, the outline, and the metadata title. Build turns those
// primitives into structure, choosing among three sources in priority order:
//
//  1. the embedded outline;
//  2. a printed table of contents, when the outline was stripped but the
//     contents pages survive;
//  3. font sizes alone.
//
// When none yields a heading tree, Build refuses the document with
// ErrNoStructure. Silent degradation to fixed token windows is how a
// document quietly becomes unsearchable, so it is never the fallback.
//
// Build is pure, so it is held to Python exactly: fed the primitives PyMuPDF
// reported, it must produce the Python adapter's extraction. The Engine reads
// with PDFium rather than MuPDF, which report slightly different lines; its
// rules, and what still differs, are in docs/research/pdfium-spike.md.
package pdf

// Document is everything Build reads from a PDF.
type Document struct {
	// Title is the metadata title, "" when the PDF has none.
	Title string
	// Outline is the embedded outline, depth first.
	Outline []OutlineEntry
	Pages   []Page
}

// OutlineEntry is one entry of the embedded outline.
type OutlineEntry struct {
	Title string
	// Level is 1 for a top-level entry.
	Level int
	// Page is the 1-based page the entry points to, or -1 when it points
	// nowhere.
	Page int
}

// Page is one page's primitives.
type Page struct {
	// Text is the page's plain text, stored for display.
	Text string
	// Lines are sorted by top, rounded to a tenth of a point, then left.
	Lines  []Line
	Images []Image
}

// Line is one line of text, in points from the page's top left.
type Line struct {
	Text string
	Y0   float64
	X0   float64
	Y1   float64
	// Size is the font size of the line's first span, rounded to a tenth.
	Size float64
}

// Image is one placement of an image on a page.
type Image struct {
	// Y0 is the top of the placement, in points from the page's top.
	Y0 float64
	// Area is the placed area in square points.
	Area float64
	// ID is the same for every placement of the same image, so one logo
	// drawn on every page is recognisable as page furniture.
	ID int64
}
