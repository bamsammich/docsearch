package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/upload"
)

// responseBatch is how many cached responses a site copy holds in memory at
// once.
const responseBatch = 100

// Copies starts copies and moves of documents between libraries, as one
// user.
//
// A copy is an ingest job in the target library. Starting one reads the
// source and writes the target in a single transaction that switches
// app.library_id between statements, so the row policies check a reader
// role where it reads and an editor role where it writes, as they do for
// every other statement.
type Copies struct {
	db   *sql.DB
	q    *pgdbgen.Queries
	user string
}

// NewCopies starts copies through db as user.
func NewCopies(db *sql.DB, user string) *Copies {
	return &Copies{db: db, q: pgdbgen.New(db), user: user}
}

// Start queues the copy and returns its job. The stored original goes with
// it: an uploaded file, or a crawled site's cached responses where the
// person edits the source. A file under a root needs nothing, since the
// target reads the same path.
func (c *Copies) Start(ctx context.Context, req document.CopyRequest) (int64, error) {
	var job int64
	scope := pgsession.Scope{User: c.user, Library: req.From}
	err := pgsession.Run(ctx, c.db, c.q, scope, func(q *pgdbgen.Queries) error {
		var err error
		job, err = start(ctx, q, req)
		return err
	})
	return job, err
}

// start is Start inside its transaction, which begins naming the source.
func start(ctx context.Context, q *pgdbgen.Queries, req document.CopyRequest) (int64, error) {
	src, original, err := readSource(ctx, q, req)
	if err != nil {
		return 0, err
	}
	if err := q.SetLibrary(ctx, req.To); err != nil {
		return 0, fmt.Errorf("switch to the target library: %w", err)
	}
	if err := checkTarget(ctx, q, req, src); err != nil {
		return 0, err
	}
	if err := carry(ctx, q, req, src, original); err != nil {
		return 0, err
	}
	job, err := q.EnqueueCopy(ctx, pgdbgen.EnqueueCopyParams{
		SourcePath: src.SourcePath,
		Title:      nullString(src.Title),
		CopyFrom:   nullString(req.From),
		CopyDocID:  nullString(req.DocID),
		CopyAs:     nullString(req.Target()),
		Move:       req.Move,
	})
	if err != nil {
		return 0, fmt.Errorf("queue the copy: %w", err)
	}
	return job, nil
}

// carry copies the stored original into the target library.
func carry(
	ctx context.Context,
	q *pgdbgen.Queries,
	req document.CopyRequest,
	src pgdbgen.CopySourceRow,
	original *pgdbgen.GetUploadRow,
) error {
	if original != nil {
		err := q.PutUpload(ctx, pgdbgen.PutUploadParams{
			Key:      original.Key,
			Filename: original.Filename,
			Sha256:   original.Sha256,
			Content:  original.Content,
		})
		if err != nil {
			return fmt.Errorf("copy the uploaded file: %w", err)
		}
		return nil
	}
	if src.SourceKind == domain.FormatSite {
		return copySite(ctx, q, req, src.SourcePath)
	}
	return nil
}

// readSource is the document to copy, and its uploaded file where it was
// read from one, with the transaction naming the source library.
func readSource(
	ctx context.Context,
	q *pgdbgen.Queries,
	req document.CopyRequest,
) (pgdbgen.CopySourceRow, *pgdbgen.GetUploadRow, error) {
	src, err := q.CopySource(ctx, req.DocID)
	if errors.Is(err, sql.ErrNoRows) {
		return src, nil, fmt.Errorf("%w: %s", ErrNotFound, req.DocID)
	}
	if err != nil {
		return src, nil, fmt.Errorf("read %s: %w", req.DocID, err)
	}
	if src.Status != statusReady {
		return src, nil, document.ErrNotReady
	}
	if req.Move {
		if err := mayEdit(ctx, q, document.ErrCannotMove); err != nil {
			return src, nil, err
		}
	}
	original, err := uploadOf(ctx, q, src.SourcePath)
	return src, original, err
}

// mayEdit refuses with refusal unless the transaction's user edits the
// library it names.
func mayEdit(ctx context.Context, q *pgdbgen.Queries, refusal error) error {
	edits, err := q.EditsLibrary(ctx)
	if err != nil {
		return fmt.Errorf("check the library: %w", err)
	}
	if !edits {
		return refusal
	}
	return nil
}

// uploadOf is the uploaded file a document was read from, or nil for a
// document read from anything else.
func uploadOf(
	ctx context.Context,
	q *pgdbgen.Queries,
	sourcePath string,
) (*pgdbgen.GetUploadRow, error) {
	key, uploaded := upload.KeyOf(sourcePath)
	if !uploaded {
		return nil, nil //nolint:nilnil // a document read from a path or a URL has no upload
	}
	original, err := q.GetUpload(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, document.ErrOriginalGone
	}
	if err != nil {
		return nil, fmt.Errorf("read the uploaded file: %w", err)
	}
	return &original, nil
}

// checkTarget refuses a copy the target library cannot take, with the
// transaction naming the target library.
func checkTarget(
	ctx context.Context,
	q *pgdbgen.Queries,
	req document.CopyRequest,
	src pgdbgen.CopySourceRow,
) error {
	if err := mayEdit(ctx, q, document.ErrCannotAdd); err != nil {
		return err
	}
	held, err := q.ReadyDocIDWithDigest(ctx, src.Sha256)
	if err == nil {
		return document.AlreadyHeld{DocID: held}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("look for the same bytes: %w", err)
	}
	same, err := q.DocIDForSourcePath(ctx, src.SourcePath)
	if err == nil {
		return document.SourceHeld{DocID: same}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("look for the same source: %w", err)
	}
	_, err = q.DocumentStatus(ctx, req.Target())
	if err == nil {
		return document.Taken{DocID: req.Target()}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("look up %s: %w", req.Target(), err)
	}
	return nil
}

// copySite copies a crawled site's cached responses from the source library
// to the target, a batch at a time, and leaves the transaction naming the
// target. Every response on the site's host goes, because a crawl reads any
// same-host page the site declares, whatever its path.
func copySite(
	ctx context.Context,
	q *pgdbgen.Queries,
	req document.CopyRequest,
	seed string,
) error {
	prefix, err := hostPrefix(seed)
	if err != nil {
		return err
	}
	after := ""
	for {
		last, full, err := copyBatch(ctx, q, req, prefix, after)
		if err != nil || !full {
			return err
		}
		after = last
	}
}

// copyBatch copies the next batch of cached responses after the URL after,
// and reports the last URL it copied and whether the batch was full.
func copyBatch(
	ctx context.Context,
	q *pgdbgen.Queries,
	req document.CopyRequest,
	prefix, after string,
) (string, bool, error) {
	if err := q.SetLibrary(ctx, req.From); err != nil {
		return "", false, fmt.Errorf("switch to the source library: %w", err)
	}
	rows, err := q.SiteResponses(ctx, pgdbgen.SiteResponsesParams{
		Prefix: prefix, After: after, Batch: responseBatch,
	})
	if err != nil {
		return "", false, fmt.Errorf("read the cached site: %w", err)
	}
	if err := q.SetLibrary(ctx, req.To); err != nil {
		return "", false, fmt.Errorf("switch to the target library: %w", err)
	}
	for _, row := range rows {
		if err := q.CopyResponse(ctx, pgdbgen.CopyResponseParams(row)); err != nil {
			return "", false, fmt.Errorf("copy the cached %s: %w", row.Url, err)
		}
		after = row.Url
	}
	return after, len(rows) == responseBatch, nil
}

// hostPrefix is the scheme and host of a site's seed, with the slash that
// begins every path on it.
func hostPrefix(seed string) (string, error) {
	parsed, err := url.Parse(seed)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("read the site's address %q: %w", seed, err)
	}
	return parsed.Scheme + "://" + parsed.Host + "/", nil
}

// finishMove removes a moved document from the library it left, in the
// transaction that publishes its copy, and leaves the transaction naming
// the target again.
//
// The policies decide whether the person still edits the source. Where they
// no longer do, the copy stays published, the source stays where it was,
// and the job's error says so.
func finishMove(ctx context.Context, q *pgdbgen.Queries, target string, id int64) error {
	move, err := q.MoveOfJob(ctx, id)
	if err != nil {
		return fmt.Errorf("read job %d: %w", id, err)
	}
	if !move.Move {
		return nil
	}
	removed, err := leaveSource(ctx, q, move)
	if err != nil {
		return err
	}
	if err := q.SetLibrary(ctx, target); err != nil {
		return fmt.Errorf("switch to the target library: %w", err)
	}
	if removed {
		return nil
	}
	return q.NoteJob(ctx, pgdbgen.NoteJobParams{
		ID:   id,
		Note: "copied; the original stayed, because you no longer edit the library it was in",
	})
}

// leaveSource removes the moved document where the person still edits its
// library, and reports whether they did, with the transaction naming the
// source.
func leaveSource(ctx context.Context, q *pgdbgen.Queries, move pgdbgen.MoveOfJobRow) (bool, error) {
	if err := q.SetLibrary(ctx, move.CopyFrom); err != nil {
		return false, fmt.Errorf("switch to the source library: %w", err)
	}
	edits, err := q.EditsLibrary(ctx)
	if err != nil {
		return false, fmt.Errorf("check the source library: %w", err)
	}
	if !edits {
		return false, nil
	}
	return true, removeSource(ctx, q, move.CopyDocID)
}

// removeSource deletes the document a move left behind, and its uploaded
// file. A source already gone since the move was asked for leaves nothing
// to do.
func removeSource(ctx context.Context, q *pgdbgen.Queries, docID string) error {
	if err := q.DeleteDocumentUpload(ctx, docID); err != nil {
		return fmt.Errorf("delete the upload of %s: %w", docID, err)
	}
	return deleteRows(ctx, q, docID)
}
