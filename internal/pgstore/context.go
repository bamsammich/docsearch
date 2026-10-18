package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
)

// ContextChunk is a neighbouring chunk returned by get_context.
type ContextChunk struct {
	PageStart   *int   `json:"page_start,omitempty"`
	HeadingPath string `json:"heading_path"`
	Section     string `json:"section,omitempty"`
	// URL and Fragment address the page this chunk was read from. Both are
	// empty for a document ingested from a local file.
	URL        string `json:"url,omitempty"`
	Fragment   string `json:"fragment,omitempty"`
	Text       string `json:"text"`
	ChunkID    int64  `json:"chunk_id"`
	Ordinal    int    `json:"ordinal"`
	ImageCount int    `json:"image_count"`
	// IsAnchor marks the chunk the caller asked for, not a URL fragment.
	IsAnchor bool `json:"is_anchor,omitempty"`
}

// PageText is a page returned by the page-addressed form of get_context.
type PageText struct {
	Text string `json:"text"`
	Page int    `json:"page"`
}

// Roughly the combined span cap for get_context, in estimated tokens.
const contextTokenCap = 6000

// maxContextSide caps how many chunks either side of the anchor are read.
// The token cap trims the span well before this many chunks of any real
// size; the bound is on what a request makes the database read, which the
// token cap applies only afterwards.
const maxContextSide = 50

// maxContextPages caps the page-addressed form.
const maxContextPages = 20

// GetContext returns chunks around an anchor in document order.
func (s *Store) GetContext(ctx context.Context, docID string, chunkID int64,
	before, after int) ([]ContextChunk, bool, error) {
	if err := s.requireReady(ctx, docID); err != nil {
		return nil, false, err
	}
	anchor, err := s.chunkOrdinal(ctx, docID, chunkID)
	if err != nil {
		return nil, false, err
	}
	span, err := s.contextSpan(ctx, docID, anchor,
		min(max(before, 0), maxContextSide), min(max(after, 0), maxContextSide))
	if err != nil {
		return nil, false, err
	}
	span, truncated := trimToCap(span, anchor)
	return span, truncated, nil
}

// chunkOrdinal is a chunk's position in its document.
func (s *Store) chunkOrdinal(ctx context.Context, docID string, chunkID int64) (int, error) {
	ordinal, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) (int32, error) {
			return q.ChunkOrdinal(ctx, pgdbgen.ChunkOrdinalParams{ID: chunkID, DocID: docID})
		})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: chunk %d in %q", ErrNotFound, chunkID, docID)
	}
	if err != nil {
		return 0, err
	}
	return int(ordinal), nil
}

// contextSpan reads the chunks on either side of an anchor, in document order.
func (s *Store) contextSpan(ctx context.Context, docID string,
	anchor, before, after int) ([]ContextChunk, error) {
	rows, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) ([]pgdbgen.ContextChunksRow, error) {
			return q.ContextChunks(ctx, pgdbgen.ContextChunksParams{
				DocID:     docID,
				Ordinal:   pgsession.Narrow(anchor - before),
				Ordinal_2: pgsession.Narrow(anchor + after),
			})
		})
	if err != nil {
		return nil, err
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
	return out, nil
}

// trimToCap drops chunks from the front while they precede the anchor, then
// from the back, until the span fits, so the chunk the caller asked for
// always survives the cap. Each chunk is estimated once.
func trimToCap(span []ContextChunk, anchor int) ([]ContextChunk, bool) {
	tokens := make([]int, len(span))
	total := 0
	for i, c := range span {
		tokens[i] = estimateTokens(c.Text)
		total += tokens[i]
	}
	first, last := 0, len(span)-1
	for total > contextTokenCap && last > first {
		if span[first].Ordinal < anchor {
			total -= tokens[first]
			first++
		} else {
			total -= tokens[last]
			last--
		}
	}
	return span[first : last+1], first > 0 || last < len(span)-1
}

// GetPages returns raw page text for paginated documents.
func (s *Store) GetPages(
	ctx context.Context,
	docID string,
	start, end int,
) ([]PageText, bool, error) {
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
	rows, err := pgsession.Read(ctx, s.db, s.q, s.userID,
		func(q *pgdbgen.Queries) ([]pgdbgen.PagesInRangeRow, error) {
			return q.PagesInRange(ctx, pgdbgen.PagesInRangeParams{
				DocID: docID, Page: pgsession.Narrow(start), Page_2: pgsession.Narrow(end),
			})
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
