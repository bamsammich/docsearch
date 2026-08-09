package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bamsammich/docsearch/internal/store/dbgen"
)

// ContextChunk is a neighbouring chunk returned by get_context.
type ContextChunk struct {
	ChunkID     int64  `json:"chunk_id"`
	Ordinal     int    `json:"ordinal"`
	HeadingPath string `json:"heading_path"`
	Section     string `json:"section,omitempty"`
	PageStart   *int   `json:"page_start,omitempty"`
	ImageCount  int    `json:"image_count"`
	// URL and Fragment address the page this chunk was read from. Both are
	// empty for a document ingested from a local file.
	URL      string `json:"url,omitempty"`
	Fragment string `json:"fragment,omitempty"`
	Text     string `json:"text"`
	// IsAnchor marks the chunk the caller asked for, not a URL fragment.
	IsAnchor bool `json:"is_anchor,omitempty"`
}

// PageText is a page returned by the page-addressed form of get_context.
type PageText struct {
	Page int    `json:"page"`
	Text string `json:"text"`
}

// Roughly the combined span cap for get_context, in estimated tokens.
const contextTokenCap = 6000

// maxContextPages caps the page-addressed form.
const maxContextPages = 20

// GetContext returns chunks around an anchor in document order.
func (s *Store) GetContext(ctx context.Context, docID string, chunkID int64,
	before, after int) ([]ContextChunk, bool, error) {
	if err := s.requireReady(ctx, docID); err != nil {
		return nil, false, err
	}
	ordinal, err := s.q.ChunkOrdinal(ctx, dbgen.ChunkOrdinalParams{ID: chunkID, DocID: docID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("%w: chunk %d in %q", ErrNotFound, chunkID, docID)
	}
	if err != nil {
		return nil, false, err
	}
	anchor := int(ordinal)
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}

	rows, err := s.q.ContextChunks(ctx, dbgen.ContextChunksParams{
		DocID:     docID,
		Ordinal:   int64(anchor - before),
		Ordinal_2: int64(anchor + after),
	})
	if err != nil {
		return nil, false, err
	}

	var out []ContextChunk
	for _, r := range rows {
		c := ContextChunk{
			ChunkID:     r.ID,
			Ordinal:     int(r.Ordinal),
			HeadingPath: r.HeadingPath,
			Section:     r.Section.String,
			PageStart:   nullInt(r.PageStart),
			ImageCount:  int(r.ImageCount),
			URL:         r.Url.String,
			Fragment:    r.Fragment.String,
			Text:        r.Text,
		}
		c.IsAnchor = c.Ordinal == anchor
		out = append(out, c)
	}

	// Trim outward from the anchor until the span fits, so the requested
	// chunk always survives the cap.
	truncated := false
	for estimateSpan(out) > contextTokenCap && len(out) > 1 {
		truncated = true
		if out[0].Ordinal != anchor && (len(out) == 1 || out[0].Ordinal < anchor) {
			out = out[1:]
		} else {
			out = out[:len(out)-1]
		}
	}
	return out, truncated, nil
}

func estimateSpan(chunks []ContextChunk) int {
	total := 0
	for _, c := range chunks {
		total += estimateTokens(c.Text)
	}
	return total
}

// GetPages returns raw page text for paginated documents.
func (s *Store) GetPages(ctx context.Context, docID string, start, end int) ([]PageText, bool, error) {
	if err := s.requireReady(ctx, docID); err != nil {
		return nil, false, err
	}
	if end < start {
		end = start
	}
	truncated := false
	if end-start+1 > maxContextPages {
		end = start + maxContextPages - 1
		truncated = true
	}
	rows, err := s.q.PagesInRange(ctx, dbgen.PagesInRangeParams{
		DocID: docID, Page: int64(start), Page_2: int64(end),
	})
	if err != nil {
		return nil, false, err
	}
	var out []PageText
	for _, r := range rows {
		out = append(out, PageText{Page: int(r.Page), Text: r.Text})
	}
	return out, truncated, nil
}
