package integration

// The PDF engine, go-pdfium, against PyMuPDF. The two report slightly
// different lines, so the engine is not held to exact output. It is held to
// the chunk structure Python's pipeline cut from PyMuPDF, measured as the
// spike measured it: the overlap of the two sets of heading paths, and of the
// two sets of chunk starts (heading path and first page).
//
// Each document's scores are recorded once, beside its reference output, and
// a later run fails if either falls below them. Every manual the spike
// measured records 1.0, so any change to its structure fails; the journal
// papers record the gaps docs/research/pdfium-spike.md explains.
//
//	DOCSEARCH_RECORD_ENGINE=1 go test ./test/integration/
//
// records the floor for every PDF, replacing what was there.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/domain"
)

// engineFloor is what engine.json holds for one document.
type engineFloor struct {
	HeadingPaths float64 `json:"heading_paths"`
	ChunkStarts  float64 `json:"chunk_starts"`
}

var (
	engineOnce sync.Once
	engine     *pdf.Extractor
	engineErr  error
)

// sharedEngine starts PDFium once for the suite; starting it compiles the
// WebAssembly module, about a second.
func sharedEngine() (*pdf.Extractor, error) {
	engineOnce.Do(func() { engine, engineErr = pdf.New() })
	return engine, engineErr
}

var _ = AfterSuite(func() {
	if engine != nil {
		Expect(engine.Close()).To(Succeed())
	}
})

var _ = Describe("The PDF engine against PyMuPDF", func() {
	dir, err := parityDir()
	if err != nil {
		It("has reference output to compare against", func() { Skip(err.Error()) })
		return
	}
	docs, err := os.ReadDir(dir)
	if err != nil {
		It("reads the reference output", func() { Fail(fmt.Sprintf("read %s: %v", dir, err)) })
		return
	}
	record := os.Getenv("DOCSEARCH_RECORD_ENGINE") != ""
	for _, doc := range docs {
		docDir := filepath.Join(dir, doc.Name())
		if _, statErr := os.Stat(filepath.Join(docDir, "primitives.json")); statErr != nil {
			continue
		}
		It(doc.Name()+" keeps the chunk structure PyMuPDF gave", func() {
			got := engineScores(docDir)
			floorPath := filepath.Join(docDir, "engine.json")
			if record {
				raw, err := json.Marshal(got)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(floorPath, raw, 0o600)).To(Succeed())
				Skip(fmt.Sprintf("recorded heading paths %.4f, chunk starts %.4f",
					got.HeadingPaths, got.ChunkStarts))
			}
			var floor engineFloor
			if _, err := os.Stat(floorPath); err != nil {
				Skip("no recorded floor; run once with DOCSEARCH_RECORD_ENGINE=1")
			}
			Expect(readJSON(floorPath, &floor)).To(Succeed())
			Expect(got.HeadingPaths).To(BeNumerically(">=", floor.HeadingPaths), "heading paths")
			Expect(got.ChunkStarts).To(BeNumerically(">=", floor.ChunkStarts), "chunk starts")
		})
	}
})

// engineScores extracts the document with the Go engine and scores its
// chunks against Python's.
func engineScores(docDir string) engineFloor {
	var source struct {
		Path string `json:"path"`
	}
	Expect(readJSON(filepath.Join(docDir, "extraction.json"), &source)).To(Succeed())
	var want []domain.Chunk
	Expect(readJSON(filepath.Join(docDir, "chunks.json"), &want)).To(Succeed())

	ex, err := sharedEngine()
	Expect(err).NotTo(HaveOccurred())
	ext, err := ex.Extract(context.Background(), source.Path)
	Expect(err).NotTo(HaveOccurred())
	got := domain.Chunks(*ext)
	return engineFloor{
		HeadingPaths: jaccard(headingPaths(got), headingPaths(want)),
		ChunkStarts:  jaccard(chunkStarts(got), chunkStarts(want)),
	}
}

func headingPaths(chunks []domain.Chunk) map[string]bool {
	out := map[string]bool{}
	for _, c := range chunks {
		out[c.HeadingPath] = true
	}
	return out
}

func chunkStarts(chunks []domain.Chunk) map[string]bool {
	out := map[string]bool{}
	for _, c := range chunks {
		page := -1
		if c.PageStart != nil {
			page = *c.PageStart
		}
		out[fmt.Sprintf("%s|%d", c.HeadingPath, page)] = true
	}
	return out
}

// jaccard is the size of the intersection over the size of the union, 1
// when both are empty.
func jaccard(a, b map[string]bool) float64 {
	inter, union := 0, len(b)
	for k := range a {
		if b[k] {
			inter++
		} else {
			union++
		}
	}
	if union == 0 {
		return 1
	}
	return float64(inter) / float64(union)
}
