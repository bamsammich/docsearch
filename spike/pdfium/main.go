// Command pdfium-spike dumps the primitives docsearch's PDF adapter reads, using
// go-pdfium in WebAssembly mode, in the JSON shape spike/pdfium/replay.py
// replays through the Python pipeline.
//
// The adapter reads five things: text lines with a position and a font size,
// plain page text, image placements with an identity that repeats across
// pages, the outline, and the metadata title. PyMuPDF hands over lines
// directly. PDFium hands over runs of same-font text, so this program groups
// runs into lines, and how well that grouping matches MuPDF's is most of what
// the spike measures.
//
//	go run . -pdf FILE.pdf -out PREFIX
//
// writes PREFIX.nominal.json and PREFIX.rendered.json, which differ only in
// which font size a line carries: PDFium's nominal size, or the size after the
// text matrix is applied.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/structs"
	"github.com/klippa-app/go-pdfium/webassembly"
)

type page struct {
	Lines  [][]any `json:"lines"`  // [y0, x0, y1, size, text]
	Text   string  `json:"text"`   //
	Images [][]any `json:"images"` // [y0, identity, area]
}

type dump struct {
	Engine    string   `json:"engine"`
	PageCount int      `json:"page_count"`
	Title     string   `json:"title"`
	TOC       [][]any  `json:"toc"` // [level, title, page]
	Pages     []page   `json:"pages"`
	Seconds   float64  `json:"seconds"`
	Notes     []string `json:"notes"`
}

// run is one same-font stretch of text on one line, as PDFium reports it.
type run struct {
	top, left, bottom, right float64
	nominal, rendered        float64
	font                     string // font name, when this PDFium build reports it
	weight                   int
	text                     string
	spaceBefore, spaceAfter  bool // PDFium put whitespace at that edge of the run
}

// gapFactor is the horizontal gap, in multiples of the font size, that splits
// two runs in one band into separate lines. MuPDF keeps a printed contents
// row's "2.1." apart from its title, and the contents parser depends on it.
var gapFactor = flag.Float64("gap", 1.0, "line-split gap as a multiple of font size")

// spaceFactor is the gap, in multiples of the font size, above which two runs
// on one line are separate words. A word space is roughly a quarter em.
var spaceFactor = flag.Float64("space", 0.15, "word-space gap as a multiple of font size")

var debugPage = flag.Int("debug", -1, "print raw runs for this 0-based page to stderr")

var spanSpace = flag.Bool("spanspace", true, "always put a space where the font or size changes")

// cellRule and cellFactor split two same-font runs whose gap exceeds a word
// space: see lines. 0.45em sits between grandMA2's glyph-positioned footer,
// whose word gaps reach about 0.35em, and its contents rows, where a long
// section number leaves about 0.48em before the title.
var (
	cellRule   = flag.Bool("cell", true, "split same-font runs separated by more than a word space")
	cellFactor = flag.Float64("cellgap", 0.45, "same-font split gap as a multiple of font size")
)

func main() {
	pdfPath := flag.String("pdf", "", "PDF to read")
	out := flag.String("out", "", "output prefix")
	flag.Parse()
	if *pdfPath == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}

	initStart := time.Now()
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	inst, err := pool.GetInstance(30 * time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer inst.Close()
	initSeconds := time.Since(initStart).Seconds()

	data, err := os.ReadFile(*pdfPath)
	if err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	nominal, rendered, err := extract(inst, data)
	if err != nil {
		log.Fatal(err)
	}
	secs := math.Round(time.Since(start).Seconds()*100) / 100
	note := fmt.Sprintf("wasm runtime init %.2fs, not included in seconds", initSeconds)
	for suffix, d := range map[string]*dump{"nominal": nominal, "rendered": rendered} {
		d.Seconds = secs
		d.Notes = append(d.Notes, note)
		b, err := json.Marshal(d)
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(*out+"."+suffix+".json", b, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("%s: %d pages in %.2fs (+%.2fs init)\n", *pdfPath, nominal.PageCount, secs, initSeconds)
}

func extract(inst pdfium.Pdfium, data []byte) (*dump, *dump, error) {
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, nil, err
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})

	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, nil, err
	}
	title, err := inst.FPDF_GetMetaText(&requests.FPDF_GetMetaText{Document: doc.Document, Tag: "Title"})
	if err != nil {
		return nil, nil, err
	}
	marks, err := inst.GetBookmarks(&requests.GetBookmarks{Document: doc.Document})
	if err != nil {
		return nil, nil, err
	}
	toc := [][]any{}
	flatten(marks.Bookmarks, 1, &toc)

	nominal := &dump{Engine: "pdfium-nominal", PageCount: count.PageCount, Title: title.Value, TOC: toc}
	rendered := &dump{Engine: "pdfium-rendered", PageCount: count.PageCount, Title: title.Value, TOC: toc}

	for i := 0; i < count.PageCount; i++ {
		byIndex := requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}}
		structured, err := inst.GetPageTextStructured(&requests.GetPageTextStructured{
			Page:                   byIndex,
			Mode:                   requests.GetPageTextStructuredModeRects,
			CollectFontInformation: true,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("page %d text: %w", i+1, err)
		}
		plain, err := inst.GetPageText(&requests.GetPageText{Page: byIndex})
		if err != nil {
			return nil, nil, fmt.Errorf("page %d plain text: %w", i+1, err)
		}
		images, err := pageImages(inst, doc.Document, i)
		if err != nil {
			return nil, nil, fmt.Errorf("page %d images: %w", i+1, err)
		}
		height, err := inst.FPDF_GetPageHeightF(&requests.FPDF_GetPageHeightF{Page: byIndex})
		if err != nil {
			return nil, nil, fmt.Errorf("page %d height: %w", i+1, err)
		}
		width, err := inst.FPDF_GetPageWidthF(&requests.FPDF_GetPageWidthF{Page: byIndex})
		if err != nil {
			return nil, nil, fmt.Errorf("page %d width: %w", i+1, err)
		}
		runs := toRuns(structured.Rects, float64(width.PageWidth), float64(height.PageHeight))
		if *debugPage == i {
			for _, r := range runs {
				fmt.Fprintf(os.Stderr, "run top=%.1f left=%.1f right=%.1f size=%.1f/%.1f font=%q weight=%d text=%q\n",
					r.top, r.left, r.right, r.nominal, r.rendered, r.font, r.weight, r.text)
			}
		}
		nominal.Pages = append(nominal.Pages, page{Lines: lines(runs, false), Text: plain.Text, Images: images})
		rendered.Pages = append(rendered.Pages, page{Lines: lines(runs, true), Text: plain.Text, Images: images})
	}
	return nominal, rendered, nil
}

// flatten turns the bookmark tree into PyMuPDF's get_toc(simple=True) rows.
func flatten(marks []responses.GetBookmarksBookmark, level int, out *[][]any) {
	for _, m := range marks {
		pg := -1
		if m.DestInfo != nil {
			pg = m.DestInfo.PageIndex + 1
		}
		*out = append(*out, []any{level, m.Title, pg})
		flatten(m.Children, level+1, out)
	}
}

// toRuns converts PDFium's rects to top-down page coordinates, which is what
// MuPDF reports and what the adapter's y comparisons assume. PDFium's Top and
// Bottom are PDF user space, origin at the bottom left, so Top > Bottom.
//
// Runs whose centre falls outside the page are dropped. MuPDF clips extraction
// to the page by default and PDFium does not: grandMA2 carries a stray "1"
// below the bottom edge of 1,846 pages, which normalizes to a boilerplate "#"
// and takes every printed contents page number down with it.
func toRuns(rects []*responses.GetPageTextStructuredRect, pageWidth, pageHeight float64) []run {
	out := make([]run, 0, len(rects))
	for _, r := range rects {
		raw := strings.ReplaceAll(r.Text, "\r\n", " ")
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		cx := (r.PointPosition.Left + r.PointPosition.Right) / 2
		cy := pageHeight - (r.PointPosition.Top+r.PointPosition.Bottom)/2
		if cx < 0 || cx > pageWidth || cy < 0 || cy > pageHeight {
			continue
		}
		var nominal, rendered float64
		var font string
		var weight int
		if fi := r.FontInformation; fi != nil {
			nominal, rendered, font, weight = fi.Size, fi.RenderedSize, fi.Name, fi.Weight
		}
		p := r.PointPosition
		out = append(out, run{
			top: pageHeight - p.Top, left: p.Left, bottom: pageHeight - p.Bottom, right: p.Right,
			nominal: nominal, rendered: rendered, font: font, weight: weight, text: text,
			spaceBefore: raw != strings.TrimLeft(raw, " \t\n"),
			spaceAfter:  raw != strings.TrimRight(raw, " \t\n"),
		})
	}
	return out
}

// lines groups runs the way MuPDF groups spans. Runs whose vertical extents
// overlap form a band; each band is read left to right and split wherever the
// horizontal gap is wide enough to be a column break. A line takes the first
// run's size, as the adapter takes the first span's.
func lines(runs []run, useRendered bool) [][]any {
	size := func(r run) float64 {
		if useRendered && r.rendered > 0 {
			return r.rendered
		}
		return r.nominal
	}
	// Vertical text, such as the "Author Manuscript" watermark down the margin
	// of every PubMed Central manuscript, overlaps every band on the page and
	// would glue itself onto each one. MuPDF reports it as a line of its own,
	// so it bypasses banding here.
	sorted := make([]run, 0, len(runs))
	var vertical []run
	for _, r := range runs {
		if len([]rune(r.text)) > 2 && r.bottom-r.top > 2*(r.right-r.left) {
			vertical = append(vertical, r)
			continue
		}
		sorted = append(sorted, r)
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].top < sorted[j].top })

	var bands [][]run
	var bandTop, bandBottom float64
	for _, r := range sorted {
		if n := len(bands); n > 0 {
			h := math.Min(bandBottom-bandTop, r.bottom-r.top)
			overlap := math.Min(bandBottom, r.bottom) - math.Max(bandTop, r.top)
			if h > 0 && overlap >= 0.5*h {
				bands[n-1] = append(bands[n-1], r)
				bandTop, bandBottom = math.Min(bandTop, r.top), math.Max(bandBottom, r.bottom)
				continue
			}
		}
		bands = append(bands, []run{r})
		bandTop, bandBottom = r.top, r.bottom
	}

	out := [][]any{}
	// Lines in a band that share a font size take one extent. PDFium's boxes
	// hug the glyphs, so "2.2." and "System Requirements" on one baseline get
	// tops a point apart; MuPDF derives a line's box from the font's metrics
	// at the baseline, so both get the same box, and the contents parser
	// buckets rows by that top. Only same-size lines share: a 12pt heading in
	// one column and 9pt table text in the other would otherwise get one top,
	// and the adapter, which keys unnumbered headings by (page, top), would
	// mark the wrong line.
	var top, bottom float64
	extent := map[float64][2]float64{}
	emit := func(cur []run) {
		var b strings.Builder
		for i, r := range cur {
			if i > 0 {
				prev := cur[i-1]
				// The adapter joins MuPDF's spans with a space, and MuPDF
				// starts a span at every font change, so a change of font or
				// size always gets a space ("Finkenauer 2 ,"). Same-font runs
				// are letter-spaced text arriving one glyph per run: join those
				// without a space unless the gap is a word space, or "Phone"
				// becomes "P h o n e" and its digits normalize into a bare "#"
				// that the boilerplate detector then strips from every contents
				// page.
				sameFont := prev.font == r.font && prev.weight == r.weight && size(prev) == size(r)
				wordGap := r.left-prev.right > *spaceFactor*math.Max(size(prev), 1)
				if (*spanSpace && !sameFont) || wordGap || prev.spaceAfter || r.spaceBefore {
					b.WriteByte(' ')
				}
			}
			b.WriteString(r.text)
		}
		sz := math.Round(size(cur[0])*10) / 10
		if e, ok := extent[sz]; ok {
			top, bottom = e[0], e[1]
		}
		out = append(out, []any{top, cur[0].left, bottom, sz, b.String()})
	}
	for _, band := range bands {
		clear(extent)
		for _, r := range band {
			sz := math.Round(size(r)*10) / 10
			e, ok := extent[sz]
			if !ok {
				e = [2]float64{r.top, r.bottom}
			}
			extent[sz] = [2]float64{math.Min(e[0], r.top), math.Max(e[1], r.bottom)}
		}
		sort.SliceStable(band, func(i, j int) bool { return band[i].left < band[j].left })
		start := 0
		for i := 1; i < len(band); i++ {
			prev, cur := band[i-1], band[i]
			gap, em := cur.left-prev.right, math.Max(size(prev), 1)
			// PDFium already merges continuous same-font text into one run, so
			// two same-font runs side by side are separate only because the
			// text jumped: a tab stop, a table cell, a contents row's title
			// after its number. A font change mid-sentence stays joined.
			sameFont := prev.font == cur.font && prev.weight == cur.weight && size(prev) == size(cur)
			if gap > *gapFactor*em || (*cellRule && sameFont && gap > *cellFactor*em) {
				emit(band[start:i])
				start = i
			}
		}
		emit(band[start:])
	}
	for _, r := range vertical {
		top, bottom = r.top, r.bottom
		emit([]run{r})
	}
	return out
}

// pageImages finds every image object on a page, including those nested in
// form XObjects, and reports each as [top-down y0, content hash, placed area].
// The hash stands in for PyMuPDF's xref: the same logo drawn on 400 pages has
// the same bytes on each.
func pageImages(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, index int) ([][]any, error) {
	loaded, err := inst.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: doc, Index: index})
	if err != nil {
		return nil, err
	}
	defer inst.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: loaded.Page})
	pg := requests.Page{ByReference: &loaded.Page}
	height, err := inst.FPDF_GetPageHeightF(&requests.FPDF_GetPageHeightF{Page: pg})
	if err != nil {
		return nil, err
	}
	n, err := inst.FPDFPage_CountObjects(&requests.FPDFPage_CountObjects{Page: pg})
	if err != nil {
		return nil, err
	}
	out := [][]any{}
	for i := 0; i < n.Count; i++ {
		obj, err := inst.FPDFPage_GetObject(&requests.FPDFPage_GetObject{Page: pg, Index: i})
		if err != nil {
			return nil, err
		}
		if err := visit(inst, obj.PageObject, identity(), float64(height.PageHeight), &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func visit(inst pdfium.Pdfium, obj references.FPDF_PAGEOBJECT, m structs.FPDF_FS_MATRIX, pageHeight float64, out *[][]any) error {
	typ, err := inst.FPDFPageObj_GetType(&requests.FPDFPageObj_GetType{PageObject: obj})
	if err != nil {
		return err
	}
	switch typ.Type {
	case enums.FPDF_PAGEOBJ_IMAGE:
		b, err := inst.FPDFPageObj_GetBounds(&requests.FPDFPageObj_GetBounds{PageObject: obj})
		if err != nil {
			return err
		}
		left, bottom, right, top := transform(m, float64(b.Left), float64(b.Bottom), float64(b.Right), float64(b.Top))
		raw, err := inst.FPDFImageObj_GetImageDataRaw(&requests.FPDFImageObj_GetImageDataRaw{ImageObject: obj})
		id := "unreadable"
		if err == nil {
			sum := sha256.Sum256(raw.Data)
			id = hex.EncodeToString(sum[:8])
		}
		area := math.Abs(right-left) * math.Abs(top-bottom)
		*out = append(*out, []any{pageHeight - top, id, area})
	case enums.FPDF_PAGEOBJ_FORM:
		fm, err := inst.FPDFPageObj_GetMatrix(&requests.FPDFPageObj_GetMatrix{PageObject: obj})
		if err != nil {
			return err
		}
		inner := multiply(fm.Matrix, m)
		n, err := inst.FPDFFormObj_CountObjects(&requests.FPDFFormObj_CountObjects{PageObject: obj})
		if err != nil {
			return err
		}
		for i := 0; i < n.Count; i++ {
			child, err := inst.FPDFFormObj_GetObject(&requests.FPDFFormObj_GetObject{PageObject: obj, Index: uint64(i)})
			if err != nil {
				return err
			}
			if err := visit(inst, child.PageObject, inner, pageHeight, out); err != nil {
				return err
			}
		}
	}
	return nil
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

// transform maps a bounding box through m and returns the enclosing box.
func transform(m structs.FPDF_FS_MATRIX, l, b, r, t float64) (float64, float64, float64, float64) {
	xs, ys := make([]float64, 0, 4), make([]float64, 0, 4)
	for _, p := range [][2]float64{{l, b}, {l, t}, {r, b}, {r, t}} {
		x := float64(m.A)*p[0] + float64(m.C)*p[1] + float64(m.E)
		y := float64(m.B)*p[0] + float64(m.D)*p[1] + float64(m.F)
		xs, ys = append(xs, x), append(ys, y)
	}
	sort.Float64s(xs)
	sort.Float64s(ys)
	return xs[0], ys[0], xs[3], ys[3]
}
