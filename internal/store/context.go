package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ContextChunk is a neighbouring chunk returned by get_context.
type ContextChunk struct {
	ChunkID     int64  `json:"chunk_id"`
	Ordinal     int    `json:"ordinal"`
	HeadingPath string `json:"heading_path"`
	Section     string `json:"section,omitempty"`
	PageStart   *int   `json:"page_start,omitempty"`
	ImageCount  int    `json:"image_count"`
	Text        string `json:"text"`
	IsAnchor    bool   `json:"is_anchor,omitempty"`
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
	var anchor int
	err := s.db.QueryRowContext(ctx,
		`SELECT ordinal FROM chunks WHERE id = ? AND doc_id = ?`, chunkID, docID).Scan(&anchor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("%w: chunk %d in %q", ErrNotFound, chunkID, docID)
	}
	if err != nil {
		return nil, false, err
	}
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ordinal, heading_path, section, page_start, image_count, text
		  FROM chunks
		 WHERE doc_id = ? AND ordinal BETWEEN ? AND ?
		 ORDER BY ordinal`, docID, anchor-before, anchor+after)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()

	var out []ContextChunk
	for rows.Next() {
		var c ContextChunk
		var section sql.NullString
		if err := rows.Scan(&c.ChunkID, &c.Ordinal, &c.HeadingPath, &section, &c.PageStart,
			&c.ImageCount, &c.Text); err != nil {
			return nil, false, err
		}
		if section.Valid {
			c.Section = section.String
		}
		c.IsAnchor = c.Ordinal == anchor
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
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
	rows, err := s.db.QueryContext(ctx,
		`SELECT page, text FROM pages WHERE doc_id = ? AND page BETWEEN ? AND ? ORDER BY page`,
		docID, start, end)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	var out []PageText
	for rows.Next() {
		var p PageText
		if err := rows.Scan(&p.Page, &p.Text); err != nil {
			return nil, false, err
		}
		out = append(out, p)
	}
	return out, truncated, rows.Err()
}
