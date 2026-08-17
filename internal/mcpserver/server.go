// Package mcpserver registers the seven document tools on an MCP server.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bamsammich/docsearch/internal/libroot"
	"github.com/bamsammich/docsearch/internal/store"
	"github.com/bamsammich/docsearch/internal/urlguard"
)

// Deps are what the tools need to do their work.
type Deps struct {
	Store *store.Store
	// LibraryRoots are the directories add_document accepts a path inside.
	// They are named in that tool's description: a caller that can see
	// neither the roots nor why a path was refused has no way to recover
	// from the deliberately uninformative rejection, which is what made the
	// tool usable only on files a person had already staged.
	LibraryRoots []string
	Log          *slog.Logger
	// Resolver looks up hosts for the address guard. nil takes the system
	// resolver; tests supply their own, the same way urlguard's own suite
	// does, because the guard refuses every address a test server can bind.
	Resolver urlguard.Resolver
}

// toolMaxK is the search tool's documented result cap.
const toolMaxK = 25

// describeRoots names the directories add_document accepts, in the tool's own
// description.
//
// Every rejection returns one error that says nothing about the path, which
// makes the tool impossible to use without knowing where files may live: a
// caller handed a path outside the roots cannot tell whether the file is
// missing, unreadable, or merely somewhere else, and has nowhere to move it
// to. Naming the roots costs nothing -- they are operator configuration, not a
// property of the caller's path -- and turns that dead end into an action.
func describeRoots(roots []string) string {
	switch len(roots) {
	case 0:
		// Validate rejects this at startup; a server with no roots cannot
		// accept any path at all.
		return "This server has no library root configured, so add_document " +
			"cannot accept any path."
	case 1:
		return "The path must be inside " + roots[0] + ". A file elsewhere has to be " +
			"copied there first; tell the user rather than guessing at another path."
	default:
		return "The path must be inside one of: " + strings.Join(roots, ", ") +
			". A file elsewhere has to be copied into one of them first; tell the user " +
			"rather than guessing at another path."
	}
}

// describeAddDocument is the tool's whole description, assembled where it can
// be read by a test. A caller holding a URL has no reason to try one unless
// the description says it is accepted, and a caller holding a path outside the
// roots has nowhere to go unless it names them.
func describeAddDocument(roots []string) string {
	return "Queue a document or a documentation site for ingest and return immediately.\n\n" +
		"Ingest is ASYNCHRONOUS and slow: a large PDF or a site of any size takes minutes " +
		"to tens of minutes. This call only enqueues the job. Nothing is searchable when " +
		"it returns. Tell the user it has been queued, not that it is ready, and poll " +
		"ingest_status to find out when it completes.\n\n" +
		"'target' is either a filesystem path or an http(s) URL.\n\n" +
		"A URL is crawled as ONE document: the site's own navigation supplies the section " +
		"structure, every page becomes a section of it, and results carry the page URL and " +
		"anchor so an answer can be cited. Pass the documentation root rather than a " +
		"single page — https://example.com/docs, not https://example.com/docs/install — " +
		"because the crawl is scoped from what the seed declares. Only public addresses " +
		"are fetched; private, loopback and local-only names are refused.\n\n" +
		describeRoots(roots)
}

// serverInstructions is sent in the initialize result and lands in the client's
// system prompt for every session, whether or not any tool is ever called.
//
// The tool descriptions can only speak once a caller has decided to look at
// docsearch, which is too late for the two habits that decide whether the
// corpus is worth having. A session that reaches for the web first never learns
// the answer was already local, and a session that ingests its own write-up
// instead of the source poisons the corpus for every session after it. Both are
// choices made before any tool call, so they have to be stated somewhere that
// is read before any tool call.
const serverInstructions = "docsearch holds the full text of documentation the user has " +
	"ingested -- manuals, specifications and documentation sites -- and searches it locally.\n\n" +
	"SEARCH HERE FIRST. Before searching the web or fetching a documentation page, call " +
	"list_documents: it is one call and it names the whole corpus, so it settles whether the " +
	"answer is already local. When a document covers the question, read its outline, then " +
	"search with section_filter set to the chapter that covers it. Go to the web only once " +
	"the corpus has been checked and does not hold the answer.\n\n" +
	"INGEST WHAT YOU READ. When research settles on a documentation site or a manual worth " +
	"having again, queue it with add_document so the next session finds it locally. Ingest " +
	"the source itself -- the documentation root URL, or the file -- and never your own " +
	"summary, notes or synthesis of it. An interpretation in the corpus is worse than " +
	"nothing: it displaces the primary text a later search should have found, and carries " +
	"one session's compression forward as though it were the source.\n\n" +
	"Ingest is asynchronous and slow. add_document returns as soon as the job is queued and " +
	"nothing is searchable at that point. Queue it, say that it is queued, and carry on with " +
	"the task; check ingest_status later rather than waiting on it."

var supportedSuffixes = map[string]bool{
	".pdf": true, ".md": true, ".markdown": true, ".html": true,
	".htm": true, ".docx": true, ".txt": true, ".text": true,
}

// New builds an MCP server with the document tools registered.
func New(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "docsearch",
		Version: "0.1.0",
	}, &mcp.ServerOptions{Instructions: serverInstructions})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_documents",
		Description: "List every document available to search, with its top-level headings " +
			"so you can pick the right one without a separate outline call. " +
			"Each document reports a quality flag from ingest: 'ok' means the derived " +
			"heading structure was validated against the document's own table of contents; " +
			"'degraded' means structural anomalies were recorded and section attribution " +
			"may be unreliable for some chunks — the specific findings are in 'warnings'.",
	}, d.listDocuments)

	mcp.AddTool(s, &mcp.Tool{
		Name: "outline",
		Description: "Return a document's heading tree to the given depth.\n\n" +
			"CALL THIS BEFORE SEARCHING. Cold keyword search is the weakest way to use " +
			"these tools, especially when your phrasing is everyday language rather than " +
			"the document's own terminology. Read the outline, choose the chapter that " +
			"plainly covers the question, then search again with section_filter set to " +
			"that chapter heading.",
	}, d.outline)

	mcp.AddTool(s, &mcp.Tool{
		Name: "search",
		Description: "Full-text BM25 search over document chunks, weighting section headings " +
			"above body text. Returns the full chunk text, not a snippet.\n\n" +
			"This is lexical search: it matches words, not meaning. If your phrasing is " +
			"everyday language rather than the document's own vocabulary, a cold search " +
			"will often miss. Read `outline` first and pass section_filter. If a search " +
			"disappoints, do not rephrase and retry blindly; orient, then scope.\n\n" +
			"'relevance' is 0..1, higher is better. It is comparable WITHIN one scoped " +
			"result set and not across documents: BM25's IDF is computed over the whole " +
			"index, so the same relevance figure means different things in a large and a " +
			"small document. An unscoped search merges each document's results by their " +
			"within-document rank rather than by score, so no cross-document score " +
			"comparison is made on your behalf.\n\n" +
			"Entries from a document's command-keyword reference are deprioritised by " +
			"default: they are term-dense and match on incidental word overlap. Pass " +
			"include_keyword_reference when you are looking up a specific command or " +
			"keyword by name.\n\n" +
			"Each result carries image_count. A result with a high image_count and little " +
			"text is a figure-dominated section: its real content is a screenshot or " +
			"diagram that is NOT in the text you receive. Do not conclude the section is " +
			"empty or irrelevant from the text alone — say that the answer appears to be " +
			"in a figure, and cite the page so the user can look at it.",
	}, d.search)

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_context",
		Description: "Return the chunks surrounding a search hit, in document order. Use this " +
			"when a result lands in the right section but the answer continues past the " +
			"chunk boundary. For paginated documents you may instead pass page_start and " +
			"page_end to read raw page text.",
	}, d.getContext)

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_document",
		Description: describeAddDocument(d.LibraryRoots),
	}, d.addDocument)

	mcp.AddTool(s, &mcp.Tool{
		Name: "ingest_status",
		Description: "Report ingest job progress. With job_id, returns that job; without, " +
			"returns all active jobs, plus recently finished ones when include_completed " +
			"is set. A completed job reports its doc_id so you can search it immediately, " +
			"and any structural warnings recorded during ingest.",
	}, d.ingestStatus)

	mcp.AddTool(s, &mcp.Tool{
		Name: "cancel_ingest",
		Description: "Request cancellation of a queued or running ingest job. Returns " +
			"immediately; the worker stops at its next checkpoint, so the job may still " +
			"read as running briefly afterwards.",
	}, d.cancelIngest)

	return s
}

// -- list_documents -------------------------------------------------------

type listDocumentsInput struct{}

type listDocumentsOutput struct {
	Documents []store.Document `json:"documents"`
}

func (d Deps) listDocuments(ctx context.Context, _ *mcp.CallToolRequest,
	_ listDocumentsInput) (*mcp.CallToolResult, listDocumentsOutput, error) {
	docs, err := d.Store.ListDocuments(ctx)
	if err != nil {
		return nil, listDocumentsOutput{}, err
	}
	return nil, listDocumentsOutput{Documents: docs}, nil
}

// -- outline --------------------------------------------------------------

type outlineInput struct {
	DocID string `json:"doc_id" jsonschema:"the document to outline"`
	Depth int    `json:"depth,omitempty" jsonschema:"heading depth to return, default 2"`
}

type outlineOutput struct {
	DocID   string               `json:"doc_id"`
	Entries []store.OutlineEntry `json:"entries"`
}

func (d Deps) outline(ctx context.Context, _ *mcp.CallToolRequest,
	in outlineInput) (*mcp.CallToolResult, outlineOutput, error) {
	if in.DocID == "" {
		return nil, outlineOutput{}, errors.New("doc_id is required")
	}
	depth := in.Depth
	if depth <= 0 {
		depth = 2
	}
	entries, err := d.Store.Outline(ctx, in.DocID, depth)
	if err != nil {
		return nil, outlineOutput{}, err
	}
	return nil, outlineOutput{DocID: in.DocID, Entries: entries}, nil
}

// -- search ---------------------------------------------------------------

type searchInput struct {
	Query                   string `json:"query" jsonschema:"the search query"`
	DocID                   string `json:"doc_id,omitempty" jsonschema:"restrict to one document"`
	SectionFilter           string `json:"section_filter,omitempty" jsonschema:"heading_path prefix filter"`
	K                       int    `json:"k,omitempty" jsonschema:"max results, default 8, max 25"`
	IncludeKeywordReference bool   `json:"include_keyword_reference,omitempty" jsonschema:"include command-keyword reference entries, which are deprioritised by default"`
}

type searchOutput struct {
	Results   []store.SearchResult            `json:"results"`
	ByDoc     map[string][]store.SearchResult `json:"results_by_document,omitempty"`
	Note      string                          `json:"note,omitempty"`
	FigureHit int                             `json:"figure_dominated_results,omitempty"`
}

func (d Deps) search(ctx context.Context, _ *mcp.CallToolRequest,
	in searchInput) (*mcp.CallToolResult, searchOutput, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, searchOutput{}, errors.New("query is required")
	}
	// The documented maximum is enforced here, where it is declared to callers.
	if in.K > toolMaxK {
		in.K = toolMaxK
	}
	results, err := d.Store.Search(ctx, store.SearchParams{
		Query:                   in.Query,
		DocID:                   in.DocID,
		SectionFilter:           in.SectionFilter,
		K:                       in.K,
		IncludeKeywordReference: in.IncludeKeywordReference,
	})
	if err != nil {
		return nil, searchOutput{}, err
	}
	out := searchOutput{Results: results}

	for _, r := range results {
		if r.ImageCount >= 2 && len(r.Text) < 800 {
			out.FigureHit++
		}
	}
	if out.FigureHit > 0 {
		out.Note = fmt.Sprintf(
			"%d of these results are figure-dominated (two or more images, little text). "+
				"Their real content is in a screenshot or diagram you cannot see. Cite the "+
				"page rather than concluding from the text alone.", out.FigureHit)
	}
	// Grouping makes the multi-document nature visible rather than implying a
	// single global ranking.
	if in.DocID == "" && len(results) > 0 {
		out.ByDoc = map[string][]store.SearchResult{}
		for _, r := range results {
			out.ByDoc[r.DocID] = append(out.ByDoc[r.DocID], r)
		}
		if len(out.ByDoc) > 1 {
			note := "Results span multiple documents and were merged by within-document " +
				"rank, so relevance figures are not comparable between them. Scope to a " +
				"doc_id to rank within one document."
			if out.Note == "" {
				out.Note = note
			} else {
				out.Note += " " + note
			}
		}
	}
	return nil, out, nil
}

// -- get_context ----------------------------------------------------------

type getContextInput struct {
	DocID     string `json:"doc_id" jsonschema:"the document"`
	ChunkID   int64  `json:"chunk_id,omitempty" jsonschema:"anchor chunk from a search result"`
	Before    int    `json:"before,omitempty" jsonschema:"chunks before the anchor, default 1"`
	After     int    `json:"after,omitempty" jsonschema:"chunks after the anchor, default 1"`
	PageStart int    `json:"page_start,omitempty" jsonschema:"first page, paginated formats only"`
	PageEnd   int    `json:"page_end,omitempty" jsonschema:"last page, max 20 pages"`
}

type getContextOutput struct {
	DocID     string               `json:"doc_id"`
	Chunks    []store.ContextChunk `json:"chunks,omitempty"`
	Pages     []store.PageText     `json:"pages,omitempty"`
	Truncated bool                 `json:"truncated,omitempty"`
	Note      string               `json:"note,omitempty"`
}

func (d Deps) getContext(ctx context.Context, _ *mcp.CallToolRequest,
	in getContextInput) (*mcp.CallToolResult, getContextOutput, error) {
	if in.DocID == "" {
		return nil, getContextOutput{}, errors.New("doc_id is required")
	}
	out := getContextOutput{DocID: in.DocID}

	if in.PageStart > 0 {
		pages, truncated, err := d.Store.GetPages(ctx, in.DocID, in.PageStart, in.PageEnd)
		if err != nil {
			return nil, getContextOutput{}, err
		}
		out.Pages, out.Truncated = pages, truncated
		if truncated {
			out.Note = "page range capped at 20 pages"
		}
		return nil, out, nil
	}
	if in.ChunkID == 0 {
		return nil, getContextOutput{}, errors.New("either chunk_id or page_start is required")
	}
	before, after := in.Before, in.After
	if before == 0 && after == 0 {
		before, after = 1, 1
	}
	chunks, truncated, err := d.Store.GetContext(ctx, in.DocID, in.ChunkID, before, after)
	if err != nil {
		return nil, getContextOutput{}, err
	}
	out.Chunks, out.Truncated = chunks, truncated
	if truncated {
		out.Note = "span trimmed to fit the context cap; the anchor chunk was preserved"
	}
	return nil, out, nil
}

// -- add_document ---------------------------------------------------------

type addDocumentInput struct {
	Target string `json:"target" jsonschema:"a file or directory inside a library root, or an http(s) URL of a documentation site"`
	Title  string `json:"title,omitempty" jsonschema:"override the derived document title"`
}

// isURL reports whether a target names something to fetch rather than a path
// on disk.
//
// Any scheme at all counts, not only http and https. A path never carries one,
// so anything that does was meant as a URL and belongs in front of the address
// guard -- which refuses file:, gopher: and the rest with the same error it
// gives everything else. Routing them to the path resolver instead would have
// it interpret "file:///etc/passwd" as a relative path, which is a strange way
// to reach the same refusal and a worse one to reason about.
func isURL(target string) bool {
	u, err := url.Parse(target)
	return err == nil && u.Scheme != ""
}

type queuedJob struct {
	JobID           int64  `json:"job_id"`
	Status          string `json:"status"`
	PositionInQueue int    `json:"position_in_queue"`
	SourcePath      string `json:"source_path"`
}

type addDocumentOutput struct {
	Jobs []queuedJob `json:"jobs"`
	Note string      `json:"note"`
}

func (d Deps) addDocument(ctx context.Context, _ *mcp.CallToolRequest,
	in addDocumentInput) (*mcp.CallToolResult, addDocumentOutput, error) {
	if in.Target == "" {
		return nil, addDocumentOutput{}, errors.New("target is required")
	}

	if isURL(in.Target) {
		// Validated here as well as in the worker, and again on every redirect
		// hop inside the fetcher. Three enforcement points, none of which may
		// assume another ran: a job row is not proof that anything checked it,
		// and this is the only one that sees the caller.
		if _, err := urlguard.Check(ctx, in.Target, d.Resolver); err != nil {
			// One error for every rejection, exactly as the path branch does.
			// Telling a caller that a host resolved but privately, rather than
			// not at all, turns this tool into a network scanner.
			d.Log.Warn("add_document rejected a URL the address guard refused")
			return nil, addDocumentOutput{}, urlguard.ErrBlocked
		}
		// Enqueued as given. Canonicalising a URL is the fetcher's rule and it
		// lives in one implementation; a second one here is how the two drift.
		// The worker normalises before it keys replacement on the result.
		id, pos, err := d.Store.Enqueue(ctx, in.Target, in.Title)
		if err != nil {
			return nil, addDocumentOutput{}, err
		}
		return nil, addDocumentOutput{
			Jobs: []queuedJob{
				{JobID: id, Status: "queued", PositionInQueue: pos, SourcePath: in.Target},
			},
			Note: "Queued one site. The whole site is crawled and indexed as a single " +
				"document, which takes minutes and is not searchable until it finishes. " +
				"Poll ingest_status for progress.",
		}, nil
	}

	resolved, err := libroot.Resolve(d.LibraryRoots, in.Target)
	if err != nil {
		// One error for every rejection. Distinguishing "outside the root"
		// from "does not exist" would make this tool a filesystem probe.
		d.Log.Warn("add_document rejected a path outside the library root")
		return nil, addDocumentOutput{}, libroot.ErrOutsideRoot
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, addDocumentOutput{}, libroot.ErrOutsideRoot
	}

	var targets []string
	if info.IsDir() {
		err = filepath.WalkDir(resolved, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				return nil
			}
			if supportedSuffixes[strings.ToLower(filepath.Ext(p))] {
				targets = append(targets, p)
			}
			return nil
		})
		if err != nil {
			return nil, addDocumentOutput{}, fmt.Errorf("could not enumerate directory")
		}
		if len(targets) == 0 {
			return nil, addDocumentOutput{}, errors.New("no supported documents found there")
		}
	} else {
		if !supportedSuffixes[strings.ToLower(filepath.Ext(resolved))] {
			return nil, addDocumentOutput{}, fmt.Errorf("unsupported file type")
		}
		targets = []string{resolved}
	}

	out := addDocumentOutput{}
	for _, t := range targets {
		id, pos, err := d.Store.Enqueue(ctx, t, in.Title)
		if err != nil {
			return nil, addDocumentOutput{}, err
		}
		out.Jobs = append(out.Jobs, queuedJob{
			JobID: id, Status: "queued", PositionInQueue: pos, SourcePath: t,
		})
	}
	out.Note = fmt.Sprintf(
		"Queued %d document(s). Ingest runs asynchronously in a separate worker and a "+
			"large PDF may take tens of minutes. Nothing is searchable yet. Poll "+
			"ingest_status for progress.", len(out.Jobs))
	return nil, out, nil
}

// -- ingest_status --------------------------------------------------------

type ingestStatusInput struct {
	JobID            int64 `json:"job_id,omitempty" jsonschema:"a specific job"`
	IncludeCompleted bool  `json:"include_completed,omitempty" jsonschema:"include finished jobs"`
}

type ingestStatusOutput struct {
	Jobs []store.Job `json:"jobs"`
}

func (d Deps) ingestStatus(ctx context.Context, _ *mcp.CallToolRequest,
	in ingestStatusInput) (*mcp.CallToolResult, ingestStatusOutput, error) {
	if in.JobID != 0 {
		job, err := d.Store.JobByID(ctx, in.JobID)
		if err != nil {
			return nil, ingestStatusOutput{}, err
		}
		return nil, ingestStatusOutput{Jobs: []store.Job{*job}}, nil
	}
	jobs, err := d.Store.ActiveJobs(ctx, in.IncludeCompleted, 25)
	if err != nil {
		return nil, ingestStatusOutput{}, err
	}
	return nil, ingestStatusOutput{Jobs: jobs}, nil
}

// -- cancel_ingest --------------------------------------------------------

type cancelIngestInput struct {
	JobID int64 `json:"job_id" jsonschema:"the job to cancel"`
}

type cancelIngestOutput struct {
	JobID  int64  `json:"job_id"`
	Status string `json:"status"`
	Note   string `json:"note"`
}

func (d Deps) cancelIngest(ctx context.Context, _ *mcp.CallToolRequest,
	in cancelIngestInput) (*mcp.CallToolResult, cancelIngestOutput, error) {
	if in.JobID == 0 {
		return nil, cancelIngestOutput{}, errors.New("job_id is required")
	}
	status, err := d.Store.RequestCancel(ctx, in.JobID)
	if err != nil {
		return nil, cancelIngestOutput{}, err
	}
	out := cancelIngestOutput{JobID: in.JobID, Status: status}
	switch status {
	case "queued", "running":
		out.Note = "Cancellation requested. The job has NOT stopped yet — the worker acts " +
			"at its next checkpoint, then rolls back any partial rows. It may still " +
			"report as running for a short time."
	default:
		out.Note = fmt.Sprintf("Job is already %s; nothing to cancel.", status)
	}
	return nil, out, nil
}
