// Package integration holds docsearch's BDD suites: behaviour checked across
// packages and against real inputs, as opposed to the unit tests beside each
// package.
package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bamsammich/docsearch/internal/domain"
)

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration")
}

// repoRoot is the directory holding go.mod, which is what the committed
// fixtures are addressed from: a suite runs in its own package directory.
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
	same, err := sameJSON(headGot, headWant)
	if err != nil {
		return err.Error()
	}
	if !same {
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

// sameJSON reports whether two values persist as the same JSON document.
//
// An extraction's diagnostics are a map[string]any, so a count built in Go
// is an int while the same count decoded from a golden is a float64, and
// comparing them as Go values reports a difference that no reader of the
// stored extraction could ever see. The stored form is the contract.
func sameJSON(a, b any) (bool, error) {
	normalize := func(v any) (any, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal %T: %w", v, err)
		}
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("decode %T: %w", v, err)
		}
		return out, nil
	}
	left, err := normalize(a)
	if err != nil {
		return false, err
	}
	right, err := normalize(b)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(left, right), nil
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
