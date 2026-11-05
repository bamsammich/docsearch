// Package original serves the file a document was read from, so a person can
// set it beside the text the index holds.
//
// A PDF goes out as a PDF, which a browser renders in its own viewer with no
// access to the page, and a Word file goes out as a download. Every other
// format goes out as plain text: an uploaded HTML file served as HTML would
// run its scripts under docsearch's origin, next to the reader's credential.
package original

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/libroot"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/upload"
)

// Pattern is where the handler is mounted.
const Pattern = "GET /original/{doc_id}"

// Documents finds a document in one library.
type Documents interface {
	Get(ctx context.Context, docID string) (*document.Document, error)
}

// Uploads reads one library's uploaded files.
type Uploads interface {
	Get(ctx context.Context, key string) (upload.File, error)
}

// Handler serves a document's original file.
type Handler struct {
	// Locate is the library a doc_id is in, as the scope that reads it, and
	// the identifier within that library.
	Locate func(ctx context.Context, user, docID string) (pgsession.Scope, string, error)
	// Documents builds the document lookup of one library.
	Documents func(scope pgsession.Scope) (Documents, error)
	// Uploads builds that library's upload store.
	Uploads func(scope pgsession.Scope) (Uploads, error)
	// NotFound is the sentinel Documents returns for a document it lacks.
	NotFound error
	Log      *slog.Logger
	// Roots are the library roots. A path is read only once it resolves
	// inside one, whatever the stored row says.
	Roots []string
}

// errGone is a document whose file is no longer where it was read from.
var errGone = errors.New("the original is no longer where docsearch read it")

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, err := owner.From(r.Context())
	if err != nil {
		http.Error(w, "no owner", http.StatusForbidden)
		return
	}
	scope, local, err := h.Locate(r.Context(), user, r.PathValue("doc_id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	docs, err := h.Documents(scope)
	if err != nil {
		h.fail(w, err)
		return
	}
	doc, err := docs.Get(r.Context(), local)
	if err != nil {
		h.fail(w, err)
		return
	}
	// A site's original is its URL, which the page links to directly.
	if doc.SourceKind == domain.SourceKindSite {
		http.Error(w, "a site has no stored original; open its URL", http.StatusNotFound)
		return
	}
	file, err := h.open(r.Context(), scope, doc.Source)
	if err != nil {
		h.fail(w, err)
		return
	}
	defer h.release(file)
	kind := kindOf(file.name)
	w.Header().Set("Content-Type", kind.contentType)
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType(kind.disposition, map[string]string{"filename": file.name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, file.name, file.modified, file.content)
}

// opened is an original ready to send.
type opened struct {
	modified time.Time
	content  io.ReadSeeker
	// release closes the file, where there is one.
	release func() error
	name    string
}

// open finds the original: in the database for an upload, under a library
// root for a file.
func (h Handler) open(ctx context.Context, scope pgsession.Scope, source string) (opened, error) {
	if key, ok := upload.KeyOf(source); ok {
		return h.openUpload(ctx, scope, key)
	}
	return h.openFile(source)
}

func (h Handler) openUpload(
	ctx context.Context,
	scope pgsession.Scope,
	key string,
) (opened, error) {
	store, err := h.Uploads(scope)
	if err != nil {
		return opened{}, err
	}
	file, err := store.Get(ctx, key)
	if errors.Is(err, upload.ErrNotFound) {
		return opened{}, errGone
	}
	if err != nil {
		return opened{}, err
	}
	return opened{
		content: bytes.NewReader(file.Content),
		release: func() error { return nil },
		name:    file.Filename,
	}, nil
}

func (h Handler) openFile(source string) (opened, error) {
	path, err := libroot.Resolve(h.Roots, source)
	if err != nil {
		return opened{}, errGone
	}
	f, err := os.Open(path) //nolint:gosec // libroot.Resolve confined path to a library root
	if err != nil {
		return opened{}, errGone
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		h.release(opened{release: f.Close})
		return opened{}, errGone
	}
	return opened{
		modified: info.ModTime(),
		content:  f,
		release:  f.Close,
		name:     filepath.Base(path),
	}, nil
}

// release closes an original once it is sent. A failed close loses nothing
// the reader needed, so it is logged rather than reported.
func (h Handler) release(file opened) {
	if err := file.release(); err != nil {
		h.Log.Warn("close an original", "error", err)
	}
}

func (h Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case h.NotFound != nil && errors.Is(err, h.NotFound):
		http.Error(w, "no such document", http.StatusNotFound)
	case errors.Is(err, errGone):
		http.Error(w, errGone.Error(), http.StatusNotFound)
	default:
		h.Log.Error("serve an original", "error", err)
		http.Error(w, "couldn't read the original", http.StatusInternalServerError)
	}
}

// kind is how one format leaves the server.
type kind struct {
	contentType string
	disposition string
}

// kindOf is how a file of this name is sent.
func kindOf(name string) kind {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return kind{contentType: "application/pdf", disposition: "inline"}
	case ".docx":
		return kind{
			contentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			disposition: "attachment",
		}
	default:
		return kind{contentType: "text/plain; charset=utf-8", disposition: "inline"}
	}
}
