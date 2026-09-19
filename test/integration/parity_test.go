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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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

func describe(c domain.Chunk) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Sprintf("unmarshalable chunk %d: %v", c.Ordinal, err)
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
