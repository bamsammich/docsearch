// Package judge asks a service the operator runs whether text docsearch
// found is what docsearch took it for.
//
// docsearch owns the contract, which docs/judge.md specifies, and knows
// nothing about what answers it. A deployment that sets no judge URL never
// builds a Client, and every rule decides exactly as it did before judges
// existed. One that sets a URL sends document text to that service, so the
// URL is an operator's decision and no request parameter changes it.
//
// A judge only ever narrows what the rules found. Its answers are advice
// that an ingest survives without: a judge that is down, slow or answering
// nonsense leaves the rules' result in place and is recorded in the
// document's diagnostics.
package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HeadingsPath is where a judge answers about heading candidates, below its
// base URL. The version is in the path, so a later contract can be served
// beside this one.
const HeadingsPath = "/v1/headings"

// Batch bounds how many candidates one request carries. A manual with
// hundreds of numbered lines is asked in several requests rather than one
// that a judge's own body limit might refuse.
const Batch = 200

// requestTimeout bounds one request. A judge answers about a batch of short
// lines, so a request that runs past this is a judge that is not answering.
const requestTimeout = 60 * time.Second

// maxResponse bounds a response body. A batch's answer is an ID and a
// number per candidate, far under this.
const maxResponse = 1 << 20

// Heading is one line docsearch's rules took for a section heading, with
// the lines around it for context.
type Heading struct {
	// ID names the candidate in the answer. It is unique within a request
	// and means nothing outside it.
	ID   string `json:"id"`
	Text string `json:"text"`
	// Before and After are the nearest lines with text above and below
	// the candidate on its page, empty at a page's edge.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Page is 1-based.
	Page int `json:"page"`
}

// Verdicts is what a judge answered about a set of candidates.
type Verdicts struct {
	// Heading is the probability, from 0 to 1, that each candidate is a
	// section heading, by ID. A candidate the judge left out is absent.
	Heading map[string]float64
	// Judge is how the service names itself, such as "jev-bridge/1
	// jev-latest", recorded so a document's diagnostics say who judged it.
	Judge string
}

// ErrBadAnswer is a response that does not keep the contract.
var ErrBadAnswer = errors.New("the judge's answer does not keep the contract")

// Client asks one judge.
type Client struct {
	http  *http.Client
	base  string
	token string
}

// New returns a client for the judge at base, an http or https URL. token,
// when set, is sent as a bearer token on every request.
func New(base, token string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("judge URL %q is not an http or https URL", base)
	}
	return &Client{
		http:  &http.Client{Timeout: requestTimeout},
		base:  strings.TrimRight(base, "/"),
		token: token,
	}, nil
}

type headingsRequest struct {
	Document   string    `json:"document"`
	Candidates []Heading `json:"candidates"`
}

type headingVerdict struct {
	// Heading is a pointer so that a verdict missing its number is told
	// from one that says 0.
	Heading *float64 `json:"heading"`
	ID      string   `json:"id"`
}

type headingsResponse struct {
	Judge    string           `json:"judge"`
	Headings []headingVerdict `json:"headings"`
}

// Headings asks whether each candidate is a section heading. document is the
// file's name, which a judge may use as context. Any failure fails the whole
// call, so a caller never acts on half of a document's answers.
func (c *Client) Headings(
	ctx context.Context,
	document string,
	candidates []Heading,
) (Verdicts, error) {
	out := Verdicts{Heading: make(map[string]float64, len(candidates))}
	for start := 0; start < len(candidates); start += Batch {
		batch := candidates[start:min(start+Batch, len(candidates))]
		answer, err := c.headings(ctx, headingsRequest{Document: document, Candidates: batch})
		if err != nil {
			return Verdicts{}, err
		}
		if err := merge(out.Heading, batch, answer.Headings); err != nil {
			return Verdicts{}, err
		}
		if out.Judge == "" {
			out.Judge = answer.Judge
		}
	}
	return out, nil
}

// merge adds a batch's verdicts to into, refusing any verdict for a
// candidate the batch did not ask about, any without a number, and any
// number that is not a probability.
func merge(into map[string]float64, asked []Heading, verdicts []headingVerdict) error {
	known := make(map[string]bool, len(asked))
	for _, h := range asked {
		known[h.ID] = true
	}
	for _, v := range verdicts {
		switch {
		case !known[v.ID]:
			return fmt.Errorf("%w: a verdict for %q, which was not asked about", ErrBadAnswer, v.ID)
		case v.Heading == nil:
			return fmt.Errorf(
				"%w: the verdict for %q has no heading probability",
				ErrBadAnswer,
				v.ID,
			)
		case *v.Heading < 0 || *v.Heading > 1:
			return fmt.Errorf("%w: %q scored %v, outside 0 to 1", ErrBadAnswer, v.ID, *v.Heading)
		}
		into[v.ID] = *v.Heading
	}
	return nil
}

func (c *Client) headings(ctx context.Context, body headingsRequest) (headingsResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return headingsResponse{}, fmt.Errorf("encode heading candidates: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+HeadingsPath,
		bytes.NewReader(payload))
	if err != nil {
		return headingsResponse{}, fmt.Errorf("ask the judge: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return headingsResponse{}, fmt.Errorf("ask the judge: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return headingsResponse{}, fmt.Errorf("ask the judge: %s answered %s",
			c.base+HeadingsPath, res.Status)
	}
	var answer headingsResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponse)).Decode(&answer); err != nil {
		return headingsResponse{}, fmt.Errorf("%w: %w", ErrBadAnswer, err)
	}
	return answer, nil
}
