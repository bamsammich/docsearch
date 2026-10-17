package pdf

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/structs"
	"github.com/klippa-app/go-pdfium/webassembly"

	"github.com/bamsammich/docsearch/internal/domain"
)

// instanceTimeout bounds the wait for the engine's one PDFium instance.
const instanceTimeout = 30 * time.Second

// Extractor reads PDFs with PDFium compiled to WebAssembly, so no cgo and
// no system library. Starting it compiles the module, about a second, so
// one Extractor serves a process; Close releases it.
type Extractor struct {
	pool pdfium.Pool
}

// New starts PDFium.
func New() (*Extractor, error) {
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	if err != nil {
		return nil, fmt.Errorf("start pdfium: %w", err)
	}
	return &Extractor{pool: pool}, nil
}

// Close stops PDFium.
func (e *Extractor) Close() error {
	if err := e.pool.Close(); err != nil {
		return fmt.Errorf("stop pdfium: %w", err)
	}
	return nil
}

// Extract reads the PDF at path into an extraction.
func (e *Extractor) Extract(ctx context.Context, path string) (*domain.Extraction, error) {
	doc, err := e.Read(ctx, path)
	if err != nil {
		return nil, err
	}
	return Build(doc, filepath.Base(path))
}

// Read reads the primitives Build needs from the PDF at path.
func (e *Extractor) Read(ctx context.Context, path string) (doc *Document, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	inst, err := e.pool.GetInstance(instanceTimeout)
	if err != nil {
		return nil, fmt.Errorf("pdfium instance: %w", err)
	}
	defer inst.Close()

	opened, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	r := reader{inst: inst, doc: opened.Document}
	defer func() {
		if _, closeErr := inst.FPDF_CloseDocument(
			&requests.FPDF_CloseDocument{Document: r.doc},
		); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", path, closeErr))
		}
	}()
	doc, err = r.document(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

// reader reads one open document.
type reader struct {
	inst pdfium.Pdfium
	doc  references.FPDF_DOCUMENT
}

func (r reader) document(ctx context.Context) (*Document, error) {
	count, err := r.inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: r.doc})
	if err != nil {
		return nil, fmt.Errorf("page count: %w", err)
	}
	title, err := r.title()
	if err != nil {
		return nil, err
	}
	marks, err := r.inst.GetBookmarks(&requests.GetBookmarks{Document: r.doc})
	if err != nil {
		return nil, fmt.Errorf("outline: %w", err)
	}
	doc := &Document{Title: title, Outline: flattenOutline(marks.Bookmarks, 1, nil)}
	for i := range count.PageCount {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		p, err := r.page(i)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		doc.Pages = append(doc.Pages, p)
	}
	return doc, nil
}

// noMetadata is the message go-pdfium returns when PDFium reports a
// zero-length value, as it does for a document with no Info dictionary. The
// error is unexported, so its text is the only way to tell "no title" from a
// failure.
const noMetadata = "Could not get metadata"

// title is the document's metadata title, "" when it has none.
func (r reader) title() (string, error) {
	meta, err := r.inst.FPDF_GetMetaText(&requests.FPDF_GetMetaText{Document: r.doc, Tag: "Title"})
	if err != nil && err.Error() == noMetadata {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("title: %w", err)
	}
	return meta.Value, nil
}

// flattenOutline lists the bookmark tree depth first, as PyMuPDF's
// get_toc(simple=True) does.
func flattenOutline(
	marks []responses.GetBookmarksBookmark,
	level int,
	out []OutlineEntry,
) []OutlineEntry {
	for _, m := range marks {
		page := -1
		if m.DestInfo != nil {
			page = m.DestInfo.PageIndex + 1
		}
		out = append(out, OutlineEntry{Title: m.Title, Level: level, Page: page})
		out = flattenOutline(m.Children, level+1, out)
	}
	return out
}

func (r reader) page(index int) (p Page, err error) {
	loaded, err := r.inst.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: r.doc, Index: index})
	if err != nil {
		return Page{}, fmt.Errorf("load: %w", err)
	}
	defer func() {
		if _, closeErr := r.inst.FPDF_ClosePage(
			&requests.FPDF_ClosePage{Page: loaded.Page},
		); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close: %w", closeErr))
		}
	}()
	return r.readPage(requests.Page{ByReference: &loaded.Page})
}

// readPage reads a loaded page's text runs, plain text and images.
func (r reader) readPage(pg requests.Page) (Page, error) {
	width, err := r.inst.FPDF_GetPageWidthF(&requests.FPDF_GetPageWidthF{Page: pg})
	if err != nil {
		return Page{}, fmt.Errorf("width: %w", err)
	}
	height, err := r.inst.FPDF_GetPageHeightF(&requests.FPDF_GetPageHeightF{Page: pg})
	if err != nil {
		return Page{}, fmt.Errorf("height: %w", err)
	}
	structured, err := r.inst.GetPageTextStructured(&requests.GetPageTextStructured{
		Page:                   pg,
		Mode:                   requests.GetPageTextStructuredModeRects,
		CollectFontInformation: true,
	})
	if err != nil {
		return Page{}, fmt.Errorf("text runs: %w", err)
	}
	plain, err := r.inst.GetPageText(&requests.GetPageText{Page: pg})
	if err != nil {
		return Page{}, fmt.Errorf("text: %w", err)
	}
	images, err := r.images(pg, float64(height.PageHeight))
	if err != nil {
		return Page{}, fmt.Errorf("images: %w", err)
	}
	runs := toRuns(structured.Rects, float64(width.PageWidth), float64(height.PageHeight))
	return Page{Text: pageText(plain.Text), Lines: buildLines(runs), Images: images}, nil
}

// pageText writes PDFium's page text the way MuPDF does, so stored page text
// reads the same whichever engine produced it: lines end in "\n" rather than
// "\r\n", the last one included.
func pageText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

// toRuns converts PDFium's text rects to runs in top-down coordinates, which
// is what MuPDF reports and what every y comparison assumes; PDFium's are PDF
// user space, origin at the bottom left.
//
// A run whose centre falls outside the page is dropped. MuPDF clips to the
// page and PDFium does not: one manual carries a stray "1" below the bottom
// edge of nearly every page, which normalizes to boilerplate "#" and takes
// every printed contents page number with it.
func toRuns(rects []*responses.GetPageTextStructuredRect, pageWidth, pageHeight float64) []run {
	out := make([]run, 0, len(rects))
	for _, rect := range rects {
		p := rect.PointPosition
		cx, cy := (p.Left+p.Right)/2, pageHeight-(p.Top+p.Bottom)/2
		if cx < 0 || cx > pageWidth || cy < 0 || cy > pageHeight {
			continue
		}
		if r := runOf(rect, pageHeight); r.text != "" {
			out = append(out, r)
		}
	}
	return out
}

// runOf converts one rect, taking the rendered font size where PDFium has
// one and the nominal size otherwise.
func runOf(rect *responses.GetPageTextStructuredRect, pageHeight float64) run {
	raw := strings.ReplaceAll(rect.Text, "\r\n", " ")
	p := rect.PointPosition
	r := run{
		text: strings.TrimSpace(raw), top: pageHeight - p.Top, left: p.Left,
		bottom: pageHeight - p.Bottom, right: p.Right,
		spaceBefore: raw != strings.TrimLeft(raw, " \t\n"),
		spaceAfter:  raw != strings.TrimRight(raw, " \t\n"),
	}
	if fi := rect.FontInformation; fi != nil {
		r.font, r.weight, r.size = fi.Name, fi.Weight, fi.RenderedSize
		if r.size <= 0 {
			r.size = fi.Size
		}
	}
	return r
}

// images finds every image on a page, including those inside form objects.
func (r reader) images(pg requests.Page, pageHeight float64) ([]Image, error) {
	n, err := r.inst.FPDFPage_CountObjects(&requests.FPDFPage_CountObjects{Page: pg})
	if err != nil {
		return nil, fmt.Errorf("count objects: %w", err)
	}
	var out []Image
	for i := range n.Count {
		obj, err := r.inst.FPDFPage_GetObject(&requests.FPDFPage_GetObject{Page: pg, Index: i})
		if err != nil {
			return nil, fmt.Errorf("object %d: %w", i, err)
		}
		if out, err = r.visit(obj.PageObject, identity(), pageHeight, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// visit adds obj to out if it is an image, placed through m, and recurses
// into a form object's children.
func (r reader) visit(
	obj references.FPDF_PAGEOBJECT,
	m structs.FPDF_FS_MATRIX,
	pageHeight float64,
	out []Image,
) ([]Image, error) {
	typ, err := r.inst.FPDFPageObj_GetType(&requests.FPDFPageObj_GetType{PageObject: obj})
	if err != nil {
		return nil, fmt.Errorf("object type: %w", err)
	}
	switch typ.Type {
	case enums.FPDF_PAGEOBJ_IMAGE:
		im, err := r.image(obj, m, pageHeight)
		if err != nil {
			return nil, err
		}
		return append(out, im), nil
	case enums.FPDF_PAGEOBJ_FORM:
		return r.visitForm(obj, m, pageHeight, out)
	}
	return out, nil
}

func (r reader) visitForm(
	obj references.FPDF_PAGEOBJECT,
	m structs.FPDF_FS_MATRIX,
	pageHeight float64,
	out []Image,
) ([]Image, error) {
	fm, err := r.inst.FPDFPageObj_GetMatrix(&requests.FPDFPageObj_GetMatrix{PageObject: obj})
	if err != nil {
		return nil, fmt.Errorf("form matrix: %w", err)
	}
	inner := multiply(fm.Matrix, m)
	n, err := r.inst.FPDFFormObj_CountObjects(&requests.FPDFFormObj_CountObjects{PageObject: obj})
	if err != nil {
		return nil, fmt.Errorf("form objects: %w", err)
	}
	for i := range n.Count {
		child, err := r.inst.FPDFFormObj_GetObject(
			&requests.FPDFFormObj_GetObject{PageObject: obj, Index: uint64(i)},
		)
		if err != nil {
			return nil, fmt.Errorf("form object %d: %w", i, err)
		}
		if out, err = r.visit(child.PageObject, inner, pageHeight, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// image is one placement. Its identity is a hash of the image's bytes, which
// stands in for MuPDF's object number: the same logo drawn on 400 pages has
// the same bytes on each. PDFium exposes no object numbers, so two stored
// copies of one image also share an identity.
func (r reader) image(
	obj references.FPDF_PAGEOBJECT,
	m structs.FPDF_FS_MATRIX,
	pageHeight float64,
) (Image, error) {
	b, err := r.inst.FPDFPageObj_GetBounds(&requests.FPDFPageObj_GetBounds{PageObject: obj})
	if err != nil {
		return Image{}, fmt.Errorf("image bounds: %w", err)
	}
	placed := transform(m, box{
		left:   float64(b.Left),
		bottom: float64(b.Bottom),
		right:  float64(b.Right),
		top:    float64(b.Top),
	})
	return Image{
		Y0:   pageHeight - placed.top,
		Area: math.Abs(placed.right-placed.left) * math.Abs(placed.top-placed.bottom),
		ID:   r.imageID(obj),
	}, nil
}

// imageID is the first six bytes of the image data's SHA-256, small enough
// to survive JSON as a number. Data PDFium cannot read gets identity 0, as
// MuPDF gives an inline image.
func (r reader) imageID(obj references.FPDF_PAGEOBJECT) int64 {
	raw, err := r.inst.FPDFImageObj_GetImageDataRaw(
		&requests.FPDFImageObj_GetImageDataRaw{ImageObject: obj},
	)
	if err != nil {
		return 0
	}
	sum := sha256.Sum256(raw.Data)
	var id int64
	for _, b := range sum[:6] {
		id = id<<8 | int64(b)
	}
	return id
}

func identity() structs.FPDF_FS_MATRIX { return structs.FPDF_FS_MATRIX{A: 1, D: 1} }

// multiply returns a then b, in PDF's row-vector convention.
func multiply(a, b structs.FPDF_FS_MATRIX) structs.FPDF_FS_MATRIX {
	return structs.FPDF_FS_MATRIX{
		A: a.A*b.A + a.B*b.C,
		B: a.A*b.B + a.B*b.D,
		C: a.C*b.A + a.D*b.C,
		D: a.C*b.B + a.D*b.D,
		E: a.E*b.A + a.F*b.C + b.E,
		F: a.E*b.B + a.F*b.D + b.F,
	}
}

// box is a rectangle in PDF user space.
type box struct {
	left, bottom, right, top float64
}

// transform maps b through m and returns the box enclosing the result.
func transform(m structs.FPDF_FS_MATRIX, b box) box {
	xs, ys := make([]float64, 0, 4), make([]float64, 0, 4)
	for _, p := range [][2]float64{{b.left, b.bottom}, {b.left, b.top}, {b.right, b.bottom}, {b.right, b.top}} {
		xs = append(xs, float64(m.A)*p[0]+float64(m.C)*p[1]+float64(m.E))
		ys = append(ys, float64(m.B)*p[0]+float64(m.D)*p[1]+float64(m.F))
	}
	return box{
		left:   slices.Min(xs),
		bottom: slices.Min(ys),
		right:  slices.Max(xs),
		top:    slices.Max(ys),
	}
}
