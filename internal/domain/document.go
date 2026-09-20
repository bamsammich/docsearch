// Package domain holds docsearch's rules: what a document is once extracted,
// how its text is sized, and how it is cut into chunks. It performs no I/O and
// knows nothing of PDF, HTML, SQLite, Postgres or MCP. Format adapters produce
// an Extraction; services persist the Chunks this package derives from it.
//
// The rules are ported from the Python pipeline under python/docsearch, which
// stays the reference until it is retired: test/integration checks every
// function here against its output on a real library.
package domain

// Block is one structural unit of a document, format-agnostic. The JSON field
// names match python/docsearch/blocks.py, so an extraction the Python pipeline
// dumps decodes here unchanged.
//
// Fields are ordered pointers first, which keeps the span the garbage
// collector scans short; the same holds for Extraction and Chunk.
type Block struct {
	// Locator is {"page": n} for paginated formats, optionally with
	// "page_end", and {"offset": n} otherwise.
	Locator map[string]int `json:"locator"`
	// Section is the numbered section this block belongs to ("5.2.1") when
	// the format supplies an authoritative numbering; nil for formats whose
	// structure is nesting only.
	Section *string `json:"section"`
	// PrintedPage is the page number printed on the page, when it differs
	// from the physical index.
	PrintedPage *int `json:"printed_page"`
	// URL and Fragment address the page a site's block was read from; both
	// nil for a local file.
	URL      *string `json:"url"`
	Fragment *string `json:"fragment"`
	Text     string  `json:"text"`
	// HeadingPath is the full heading ancestry, root first.
	HeadingPath []string `json:"heading_path"`
	// ImageCount counts raster images on the page this block came from.
	ImageCount int `json:"image_count"`
	// FigureOnly marks a caption stranded from its figure: never a chunk
	// start and never a subdivision point.
	FigureOnly bool `json:"figure_only"`
	// Subdivision marks a heading-sized line without a section number, where
	// an oversized section may split.
	Subdivision bool `json:"subdivision"`
}

// Extraction is what a format adapter returns for one source.
type Extraction struct {
	// Pages maps a page number to its text, for paginated formats.
	Pages map[int]string `json:"pages"`
	// Diagnostics are free-form notes `docsearch verify` surfaces.
	Diagnostics map[string]any `json:"diagnostics"`
	PageCount   *int           `json:"page_count"`
	Title       string         `json:"title"`
	Format      string         `json:"format"`
	Blocks      []Block        `json:"blocks"`
	// IndexTerms pairs a back-of-book index term with the section it names.
	IndexTerms [][2]string `json:"index_terms"`
}

// Chunk is one retrievable unit: the text a search returns and cites.
type Chunk struct {
	Section          *string `json:"section"`
	PageStart        *int    `json:"page_start"`
	PageEnd          *int    `json:"page_end"`
	PrintedPageStart *int    `json:"printed_page_start"`
	// URL and Fragment address the page the chunk starts on; both nil for a
	// local file.
	URL         *string   `json:"url"`
	Fragment    *string   `json:"fragment"`
	HeadingPath string    `json:"heading_path"`
	Text        string    `json:"text"`
	Ordinal     int       `json:"ordinal"`
	ImageCount  int       `json:"image_count"`
	Kind        ChunkKind `json:"kind"`
}
