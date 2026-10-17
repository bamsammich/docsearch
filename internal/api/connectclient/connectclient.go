// Package connectclient is how docsearch speaks to docsearch-server.
//
// It returns the same types the service layer works in, so a report is
// printed by the code that defines it rather than by a second formatter
// written against the wire messages. Converting here is the mirror of
// internal/api/connectapi, which converts the other way.
package connectclient

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	documentv1 "github.com/bamsammich/docsearch/internal/api/docsearch/document/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/document/v1/documentv1connect"
	ingestv1 "github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1"
	"github.com/bamsammich/docsearch/internal/api/docsearch/ingest/v1/ingestv1connect"
	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/service/job"
)

// Client speaks all three services at one server.
type Client struct {
	documents documentv1connect.DocumentServiceClient
	ingest    ingestv1connect.IngestServiceClient
	jobs      ingestv1connect.JobServiceClient
}

// New builds a client for the server at baseURL, authenticating with token.
func New(
	httpClient connect.HTTPClient,
	baseURL, token string,
	opts ...connect.ClientOption,
) *Client {
	opts = append([]connect.ClientOption{connect.WithInterceptors(bearer(token))}, opts...)
	return &Client{
		documents: documentv1connect.NewDocumentServiceClient(httpClient, baseURL, opts...),
		ingest:    ingestv1connect.NewIngestServiceClient(httpClient, baseURL, opts...),
		jobs:      ingestv1connect.NewJobServiceClient(httpClient, baseURL, opts...),
	}
}

// Default is a client over http.DefaultClient.
func Default(baseURL, token string) *Client {
	return New(http.DefaultClient, baseURL, token)
}

// bearer puts the token on every request, so no call site can forget it.
//
// A full interceptor rather than connect.UnaryInterceptorFunc, which wraps
// unary calls only: Ingest streams, and a token that covered every command
// except the one that writes documents would be the worst of both.
type bearer string

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		b.authorize(req.Header())
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		b.authorize(conn.RequestHeader())
		return conn
	}
}

// WrapStreamingHandler is the server's half, which this client never takes.
func (bearer) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return next
}

func (b bearer) authorize(header http.Header) {
	if b != "" {
		header.Set("Authorization", "Bearer "+string(b))
	}
}

// List is every document the index holds.
func (c *Client) List(ctx context.Context) ([]document.Document, error) {
	res, err := c.documents.List(ctx, connect.NewRequest(&documentv1.ListRequest{}))
	if err != nil {
		return nil, err
	}
	out := make([]document.Document, 0, len(res.Msg.GetDocuments()))
	for _, d := range res.Msg.GetDocuments() {
		out = append(out, documentOf(d))
	}
	return out, nil
}

// Verify is one document checked, both ways.
func (c *Client) Verify(ctx context.Context, docID string) (*document.VerifyReport, error) {
	res, err := c.documents.Verify(ctx,
		connect.NewRequest(&documentv1.VerifyRequest{DocId: docID}))
	if err != nil {
		return nil, err
	}
	return verifyReportOf(res.Msg.GetReport()), nil
}

// Inspect is what ingest would make of a target, having written nothing.
func (c *Client) Inspect(ctx context.Context, target string) (*domain.InspectReport, error) {
	res, err := c.documents.Inspect(ctx,
		connect.NewRequest(&documentv1.InspectRequest{Target: target}))
	if err != nil {
		return nil, err
	}
	return inspectReportOf(res.Msg.GetReport()), nil
}

// Remove deletes a document and everything stored for it.
func (c *Client) Remove(ctx context.Context, docID string) error {
	_, err := c.documents.Remove(ctx,
		connect.NewRequest(&documentv1.RemoveRequest{DocId: docID}))
	return err
}

// Enqueue puts everything a target names on the queue.
func (c *Client) Enqueue(ctx context.Context, target, title string) ([]job.Queued, error) {
	res, err := c.jobs.Enqueue(ctx, connect.NewRequest(&ingestv1.EnqueueRequest{
		Source: target,
		Title:  title,
	}))
	if err != nil {
		return nil, err
	}
	out := make([]job.Queued, 0, len(res.Msg.GetJobs()))
	for _, q := range res.Msg.GetJobs() {
		out = append(out, job.Queued{
			Source:   q.GetSource(),
			JobID:    q.GetJobId(),
			Position: int(q.GetQueuePosition()),
		})
	}
	return out, nil
}

// Jobs is the queue, newest first.
func (c *Client) Jobs(ctx context.Context, includeCompleted bool, limit int) ([]job.Job, error) {
	res, err := c.jobs.ListJobs(ctx, connect.NewRequest(&ingestv1.ListJobsRequest{
		IncludeCompleted: includeCompleted,
		Limit:            int64(limit),
	}))
	if err != nil {
		return nil, err
	}
	out := make([]job.Job, 0, len(res.Msg.GetJobs()))
	for _, j := range res.Msg.GetJobs() {
		out = append(out, jobOf(j))
	}
	return out, nil
}

// Cancel asks a job to stop and reports what it reads as now.
func (c *Client) Cancel(ctx context.Context, id int64) (string, error) {
	res, err := c.jobs.CancelJob(ctx,
		connect.NewRequest(&ingestv1.CancelJobRequest{JobId: id}))
	if err != nil {
		return "", err
	}
	return res.Msg.GetStatus(), nil
}

// Progress is how far one phase of an ingest has got. Total is 0 while a
// crawl is still discovering pages.
type Progress struct {
	Phase   string
	Current int
	Total   int
}

// Result is what one ingest did.
type Result struct {
	DocID   string
	Title   string
	Note    string
	Outcome string
	// Warnings is the structure report as it is persisted, and Diagnostics
	// is what the adapter observed. Both arrive as JSON and are rendered
	// rather than reasoned about.
	Warnings    string
	Diagnostics string
	// Findings are what the structure report made of those diagnostics.
	Findings   []string
	ChunkCount int
	Quality    domain.Quality
	SourceKind domain.SourceKind
}

// Ingest reads one source, calling onProgress as the server reports it and
// answering with the result the stream ends on.
//
// Progress is a callback rather than a channel because the caller is a
// terminal: it prints a line and returns, and a channel would add a
// goroutine and its shutdown for nothing.
func (c *Client) Ingest(
	ctx context.Context,
	source, title string,
	revalidate bool,
	onProgress func(Progress),
) (*Result, error) {
	stream, err := c.ingest.Ingest(ctx, connect.NewRequest(&ingestv1.IngestRequest{
		Source:     source,
		Title:      title,
		Revalidate: revalidate,
	}))
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()

	result := drain(stream, onProgress)
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("%s: the server ended the stream without a result", source)
	}
	return result, nil
}

// drain reads the stream to its end, reporting progress on the way and
// keeping the result, which arrives once and last.
func drain(
	stream *connect.ServerStreamForClient[ingestv1.IngestResponse],
	onProgress func(Progress),
) *Result {
	var result *Result
	for stream.Receive() {
		msg := stream.Msg()
		if p := msg.GetProgress(); p != nil && onProgress != nil {
			onProgress(Progress{
				Phase:   phaseName(p.GetPhase()),
				Current: int(p.GetCurrent()),
				Total:   int(p.GetTotal()),
			})
		}
		if r := msg.GetResult(); r != nil {
			result = resultOf(r)
		}
	}
	return result
}
