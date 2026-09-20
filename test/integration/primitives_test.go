package integration

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/domain"
)

// primitivesFile is what the local parity script dumps from PyMuPDF for one
// PDF: lines as [y0, x0, y1, size, text], images as [y0, xref, area] with the
// xref as a string, the outline as [level, title, page].
type primitivesFile struct {
	Title string           `json:"title"`
	TOC   [][]any          `json:"toc"`
	Pages []primitivesPage `json:"pages"`
}

type primitivesPage struct {
	Text   string  `json:"text"`
	Lines  [][]any `json:"lines"`
	Images [][]any `json:"images"`
}

// readPrimitives loads one PDF's dump as the document Build reads.
func readPrimitives(path string) (*pdf.Document, error) {
	var raw primitivesFile
	if err := readJSON(path, &raw); err != nil {
		return nil, err
	}
	doc := &pdf.Document{Title: raw.Title}
	for _, e := range raw.TOC {
		doc.Outline = append(doc.Outline, pdf.OutlineEntry{
			Level: int(e[0].(float64)), Title: e[1].(string), Page: int(e[2].(float64)),
		})
	}
	for _, p := range raw.Pages {
		page, err := readPage(p)
		if err != nil {
			return nil, err
		}
		doc.Pages = append(doc.Pages, page)
	}
	return doc, nil
}

func readPage(p primitivesPage) (pdf.Page, error) {
	page := pdf.Page{Text: p.Text}
	for _, ln := range p.Lines {
		page.Lines = append(page.Lines, pdf.Line{
			Y0: ln[0].(float64), X0: ln[1].(float64), Y1: ln[2].(float64),
			Size: ln[3].(float64), Text: ln[4].(string),
		})
	}
	for _, im := range p.Images {
		id, err := strconv.ParseInt(im[1].(string), 10, 64)
		if err != nil {
			return pdf.Page{}, fmt.Errorf("image identity %v: %w", im[1], err)
		}
		page.Images = append(page.Images, pdf.Image{
			Y0: im[0].(float64), ID: id, Area: im[2].(float64),
		})
	}
	return page, nil
}

// jsonDiagnostics returns ext with its diagnostics as JSON decodes them, so
// they compare with a decoded reference: Go builds ints and string slices
// where decoding gives float64 and []any.
func jsonDiagnostics(ext *domain.Extraction) *domain.Extraction {
	raw, err := json.Marshal(ext.Diagnostics)
	if err != nil {
		panic(fmt.Sprintf("diagnostics do not marshal: %v", err))
	}
	out := *ext
	out.Diagnostics = nil
	if err := json.Unmarshal(raw, &out.Diagnostics); err != nil {
		panic(fmt.Sprintf("diagnostics do not unmarshal: %v", err))
	}
	return &out
}
