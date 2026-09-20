package integration

// Parity with the Python pipeline, which stays the reference until it is
// retired. A local script, ignored by git, writes each stage of the Python
// pipeline's output for every document in a library; each spec feeds the Go
// port the stage before and requires the stage after, exactly.
//
// The reference output holds the library's text, so it lives under
// var/parity, which is ignored, and these specs skip where it has not been
// generated. DOCSEARCH_PARITY_DIR points them elsewhere.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/domain"
)

var _ = Describe("Parity with the Python pipeline", func() {
	dir, err := parityDir()
	if err != nil {
		It("has reference output to compare against", func() {
			Skip(err.Error())
		})
		return
	}
	docs, err := os.ReadDir(dir)
	if err != nil {
		It("reads the reference output", func() {
			Fail(fmt.Sprintf("read %s: %v", dir, err))
		})
		return
	}
	for _, doc := range docs {
		docDir := filepath.Join(dir, doc.Name())
		if _, statErr := os.Stat(filepath.Join(docDir, "extraction.json")); statErr != nil {
			// Python refused the document; adapter parity covers refusals.
			continue
		}
		Context(doc.Name(), func() {
			var ext domain.Extraction

			BeforeEach(func() {
				Expect(readJSON(filepath.Join(docDir, "extraction.json"), &ext)).To(Succeed())
			})

			It("extracts what the Python adapter extracts", func() {
				var source struct {
					Path string `json:"path"`
				}
				Expect(readJSON(filepath.Join(docDir, "extraction.json"), &source)).To(Succeed())
				if source.Path == "" {
					Skip("reference output predates source paths; regenerate it")
				}
				if !adapter.IsSupported(source.Path) {
					Skip("no Go adapter for " + filepath.Ext(source.Path) + " yet")
				}
				if strings.EqualFold(filepath.Ext(source.Path), ".pdf") {
					Skip("PDFium's lines differ from PyMuPDF's; the PDF specs cover PDFs")
				}
				ex, err := sharedEngine()
				Expect(err).NotTo(HaveOccurred())
				got, err := adapter.New(ex).Extract(context.Background(), source.Path)
				Expect(err).NotTo(HaveOccurred())
				Expect(extractionDifference(got, &ext)).To(BeEmpty())
			})

			It("builds the Python PDF extraction from PyMuPDF's primitives", func() {
				primitives := filepath.Join(docDir, "primitives.json")
				if _, err := os.Stat(primitives); err != nil {
					Skip("no PyMuPDF primitives: not a PDF, or the reference output predates them")
				}
				var source struct {
					Path string `json:"path"`
				}
				Expect(readJSON(filepath.Join(docDir, "extraction.json"), &source)).To(Succeed())
				doc, err := readPrimitives(primitives)
				Expect(err).NotTo(HaveOccurred())
				got, err := pdf.Build(doc, filepath.Base(source.Path))
				Expect(err).NotTo(HaveOccurred())
				Expect(extractionDifference(jsonDiagnostics(got), &ext)).To(BeEmpty())
			})

			It("counts every block's atoms as Python does", func() {
				var want struct {
					Atoms             []int   `json:"atoms"`
					UncalibratedShare float64 `json:"uncalibrated_share"`
				}
				Expect(readJSON(filepath.Join(docDir, "tokens.json"), &want)).To(Succeed())

				got := make([]int, len(ext.Blocks))
				texts := make([]string, len(ext.Blocks))
				for i, b := range ext.Blocks {
					got[i] = domain.CountAtoms(b.Text)
					texts[i] = b.Text
				}
				Expect(got).To(Equal(want.Atoms))
				Expect(domain.UncalibratedLetterShare(joinLines(texts))).
					To(Equal(want.UncalibratedShare))
			})

			It("cuts the same chunks", func() {
				var want []domain.Chunk
				Expect(readJSON(filepath.Join(docDir, "chunks.json"), &want)).To(Succeed())
				Expect(firstDifference(domain.Chunks(ext), want)).To(BeEmpty())
			})

			It("grades the structure and persists the same report", func() {
				var want struct {
					Payload        map[string]any `json:"payload"`
					FailureMessage string         `json:"failure_message"`
					Fatal          bool           `json:"fatal"`
				}
				Expect(readJSON(filepath.Join(docDir, "structure.json"), &want)).To(Succeed())

				report := domain.NewStructureReport(ext.Diagnostics)
				Expect(report.Fatal()).To(Equal(want.Fatal))
				if want.Fatal {
					Expect(report.FailureMessage()).To(Equal(want.FailureMessage))
					return
				}
				report.MeasureChunks(domain.Chunks(ext))
				raw, err := report.JSON()
				Expect(err).NotTo(HaveOccurred())
				var got map[string]any
				Expect(json.Unmarshal(raw, &got)).To(Succeed())
				Expect(got).To(Equal(want.Payload))
			})
		})
	}
})

// parityDir is DOCSEARCH_PARITY_DIR, or var/parity at the repository root,
// or an error explaining how to generate it.
func parityDir() (string, error) {
	dir := os.Getenv("DOCSEARCH_PARITY_DIR")
	if dir == "" {
		root, err := repoRoot()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(root, "var", "parity")
	}
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf(
			"no Python reference output at %s; set DOCSEARCH_PARITY_DIR to run parity",
			dir,
		)
	}
	return dir, nil
}

// repoRoot walks up from the working directory to the directory holding
// go.mod.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = up
	}
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// firstDifference describes the first chunk where got and want disagree, or
// returns "" when they match. A whole-slice Equal failure would print two
// megabytes of text; one chunk's heading and ordinal is what locates a bug.
func firstDifference(got, want []domain.Chunk) string {
	for i := range min(len(got), len(want)) {
		if !reflect.DeepEqual(got[i], want[i]) {
			return fmt.Sprintf("chunk %d differs\n  got:  %s\n  want: %s",
				i, describe(got[i]), describe(want[i]))
		}
	}
	if len(got) != len(want) {
		return fmt.Sprintf("got %d chunks, want %d", len(got), len(want))
	}
	return ""
}

// extractionDifference describes the first place got and want disagree, or
// returns "" when they match, for the same reason firstDifference does.
func extractionDifference(got, want *domain.Extraction) string {
	blocksGot, blocksWant := got.Blocks, want.Blocks
	headGot, headWant := *got, *want
	headGot.Blocks, headWant.Blocks = nil, nil
	if !reflect.DeepEqual(headGot, headWant) {
		return fmt.Sprintf("extractions differ outside their blocks\n  got:  %s\n  want: %s",
			truncatedJSON(headGot), truncatedJSON(headWant))
	}
	for i := range min(len(blocksGot), len(blocksWant)) {
		if !reflect.DeepEqual(blocksGot[i], blocksWant[i]) {
			return fmt.Sprintf("block %d differs\n  got:  %s\n  want: %s",
				i, truncatedJSON(blocksGot[i]), truncatedJSON(blocksWant[i]))
		}
	}
	if len(blocksGot) != len(blocksWant) {
		return fmt.Sprintf("got %d blocks, want %d", len(blocksGot), len(blocksWant))
	}
	return ""
}

func describe(c domain.Chunk) string {
	return truncatedJSON(c)
}

func truncatedJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("unmarshalable %T: %v", v, err)
	}
	const limit = 600
	if len(raw) > limit {
		return string(raw[:limit]) + "..."
	}
	return string(raw)
}

func joinLines(texts []string) string {
	out := ""
	for i, t := range texts {
		if i > 0 {
			out += "\n"
		}
		out += t
	}
	return out
}
