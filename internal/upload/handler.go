package upload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/bamsammich/docsearch/internal/owner"
)

// Doors serves both ways in.
type Doors struct {
	Links Links
	// Stores builds the store of the user an upload belongs to.
	Stores func(user string) (Store, error)
	// Enqueue queues target into user's library and returns the job. An
	// empty title takes the name the document gives itself.
	Enqueue func(ctx context.Context, user, target, title string) (int64, error)
	// Supports reports whether an adapter reads a file by its name.
	Supports func(filename string) bool
	Log      *slog.Logger
	// MaxBytes refuses a larger file before it is stored.
	MaxBytes int64
}

// Received is what an upload answers a program with.
type Received struct {
	Key    string `json:"key"`
	Source string `json:"source"`
	JobID  int64  `json:"job_id"`
}

// errRefused is an upload the caller can fix, answered 4xx with its reason.
type errRefused struct {
	message string
	status  int
}

func (e *errRefused) Error() string { return e.message }

func refused(status int, format string, args ...any) error {
	return &errRefused{status: status, message: fmt.Sprintf(format, args...)}
}

// Link is the signed-link door, mounted at LinkPrefix with no other
// credential: the token in the path is the credential. GET shows the page a
// person picks a file on; PUT and POST spend the link.
func (d Doors) Link() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, LinkPrefix)
		if token == "" || strings.Contains(token, "/") {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			d.page(w, r, token)
		case http.MethodPut, http.MethodPost:
			received, err := d.spend(r, token)
			d.answer(w, r, received, err)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// Direct is the authenticated door, for a program holding a token. It runs
// behind the server's guard, which named the user. A caller may name the key,
// which is how a sync replaces a document it uploaded before, and may title
// the document with ?title=.
func (d Doors) Direct() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		user, err := owner.From(r.Context())
		if err != nil {
			d.answer(w, r, Received{}, err)
			return
		}
		file, err := d.take(r, r.URL.Query().Get("key"))
		if err != nil {
			d.answer(w, r, Received{}, err)
			return
		}
		title := strings.TrimSpace(r.URL.Query().Get("title"))
		received, err := d.keep(r.Context(), user, file, title)
		d.answer(w, r, received, err)
	})
}

// spend reads the file before the link, so a file nobody can read refuses
// without using up the link the person would need to try another.
func (d Doors) spend(r *http.Request, token string) (Received, error) {
	file, err := d.take(r, "")
	if err != nil {
		return Received{}, err
	}
	user, err := d.redeem(r.Context(), token)
	if err != nil {
		return Received{}, err
	}
	return d.keep(r.Context(), user, file, "")
}

func (d Doors) redeem(ctx context.Context, token string) (string, error) {
	user, ok, err := d.Links.Redeem(ctx, hashToken(token))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", refused(http.StatusNotFound,
			"this upload link has expired or was already used; ask for a new one")
	}
	return user, nil
}

// take reads one file and refuses one no adapter reads. An empty key takes
// the content hash.
func (d Doors) take(r *http.Request, key string) (File, error) {
	filename, content, err := d.read(r)
	if err != nil {
		return File{}, err
	}
	if !d.Supports(filename) {
		return File{}, refused(http.StatusUnsupportedMediaType,
			"no reader for %q files", filepath.Ext(filename))
	}
	file := File{Key: key, Filename: filename, SHA256: Digest(content), Content: content}
	if file.Key == "" {
		file.Key = file.SHA256
	}
	return file, nil
}

// keep stores a file in user's library and queues it under title.
func (d Doors) keep(ctx context.Context, user string, file File, title string) (Received, error) {
	store, err := d.Stores(user)
	if err != nil {
		return Received{}, err
	}
	if err := store.Put(ctx, file); err != nil {
		return Received{}, err
	}
	jobID, err := d.Enqueue(ctx, user, Target(file.Key), title)
	if err != nil {
		return Received{}, err
	}
	return Received{Key: file.Key, Source: Target(file.Key), JobID: jobID}, nil
}

// read takes the file from a multipart form, as a browser sends it, or from
// the whole body, as curl -T sends it with the name in ?name=.
func (d Doors) read(r *http.Request) (string, []byte, error) {
	body := http.MaxBytesReader(nil, r.Body, d.MaxBytes)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err == nil && mediaType == "multipart/form-data" {
		r.Body = body
		return d.readForm(r)
	}
	filename := filepath.Base(r.URL.Query().Get("name"))
	if filename == "." || filename == "/" {
		return "", nil, refused(http.StatusBadRequest,
			"name the file: PUT it to this URL with ?name=<file name>")
	}
	content, err := io.ReadAll(body)
	return filename, content, d.tooLarge(err)
}

func (d Doors) readForm(r *http.Request) (string, []byte, error) {
	part, header, err := r.FormFile("file")
	if err != nil {
		if tooLarge := d.tooLarge(err); errors.As(tooLarge, new(*errRefused)) {
			return "", nil, tooLarge
		}
		return "", nil, refused(http.StatusBadRequest, "no file in the form's file field")
	}
	defer func() { _ = part.Close() }()
	content, err := io.ReadAll(part)
	return filepath.Base(header.Filename), content, d.tooLarge(err)
}

// tooLarge turns the reader's limit into a refusal that names the limit.
func (d Doors) tooLarge(err error) error {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		return refused(http.StatusRequestEntityTooLarge,
			"the file is larger than this server accepts (%d MB)", d.MaxBytes>>20)
	}
	return err
}

// answer replies in the form the caller reads: a page for a browser, JSON
// for a program. A failure the caller cannot fix is logged and kept vague.
func (d Doors) answer(w http.ResponseWriter, r *http.Request, received Received, err error) {
	status, message := http.StatusAccepted, ""
	var refusal *errRefused
	switch {
	case errors.As(err, &refusal):
		status, message = refusal.status, refusal.message
	case err != nil:
		d.Log.ErrorContext(r.Context(), "upload failed", "err", err)
		status, message = http.StatusInternalServerError, "the upload could not be stored"
	}
	if wantsPage(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		d.render(w, r, pageData{Done: err == nil, Message: message, Received: received})
		return
	}
	if err != nil {
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(received); err != nil {
		d.Log.ErrorContext(r.Context(), "write upload answer", "err", err)
	}
}

// wantsPage is a browser that submitted the form.
func wantsPage(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (d Doors) page(w http.ResponseWriter, r *http.Request, token string) {
	open, err := d.Links.Open(r.Context(), hashToken(token))
	if err != nil {
		d.Log.ErrorContext(r.Context(), "check upload link", "err", err)
		http.Error(w, "the link could not be checked", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !open {
		w.WriteHeader(http.StatusNotFound)
		d.render(w, r, pageData{Message: "This upload link has expired or was already used. " +
			"Ask Claude for a new one."})
		return
	}
	d.render(w, r, pageData{Open: true})
}

func (d Doors) render(w io.Writer, r *http.Request, data pageData) {
	if err := pageTemplate.Execute(w, data); err != nil {
		d.Log.ErrorContext(r.Context(), "render upload page", "err", err)
	}
}

// pageData is what the one page shows: a form, a result, or a refusal.
type pageData struct {
	Message  string
	Received Received
	Open     bool
	Done     bool
}

var pageTemplate = template.Must(template.New("upload").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Upload to docsearch</title>
<style>
:root { color-scheme: light dark; --ink: #1d1f1e; --ground: #f6f6f3; --rule: #cfd3cf; --accent: #2f6f5b; }
@media (prefers-color-scheme: dark) { :root { --ink: #e6e8e6; --ground: #161817; --rule: #343a37; --accent: #79c7a9; } }
body { margin: 0; padding: 48px 20px; background: var(--ground); color: var(--ink);
       font: 16px/1.5 system-ui, -apple-system, sans-serif; }
main { max-width: 34rem; margin: 0 auto; display: grid; gap: 16px; }
h1 { font-size: 1.4rem; margin: 0; }
form { display: grid; gap: 12px; padding: 20px; border: 1px solid var(--rule); border-radius: 8px; }
button { justify-self: start; padding: 8px 16px; border: 0; border-radius: 6px;
         background: var(--accent); color: var(--ground); font: inherit; cursor: pointer; }
code { font-size: 0.9em; }
</style></head><body><main>
<h1>Upload to docsearch</h1>
{{if .Open}}
<p>Choose one file. It goes into your library and is read in the background; the
link works once.</p>
<form method="post" enctype="multipart/form-data">
<input type="file" name="file" required>
<button type="submit">Upload</button>
</form>
{{else if .Done}}
<p>Uploaded. docsearch is reading it now, as job {{.Received.JobID}}. Ask Claude to
check <code>ingest_status</code>; it is searchable once the job reports ready.</p>
{{else}}
<p>{{.Message}}</p>
{{end}}
</main></body></html>
`))
