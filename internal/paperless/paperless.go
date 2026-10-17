// Package paperless reads documents from a Paperless-ngx instance through its
// REST API.
//
// It reads only what the token's user can see, which is the point: the
// operator decides what reaches docsearch by what that user is granted in
// Paperless, typically by a workflow that shares chosen document types with
// it. Nothing here writes to Paperless.
package paperless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// apiVersion is the Paperless API version these shapes were read from.
const apiVersion = "9"

// pageSize is how many documents one list request asks for.
const pageSize = 100

// fields are the document fields a sync reads. Asking for these alone keeps
// each page small: a document's OCR content can run to megabytes.
const fields = "id,title,modified,original_file_name,archived_file_name,mime_type"

// Document is what a sync needs to know about one Paperless document.
type Document struct {
	Modified time.Time `json:"modified"`
	// ArchivedFileName is empty when Paperless made no archive copy.
	ArchivedFileName string `json:"archived_file_name"`
	OriginalFileName string `json:"original_file_name"`
	MimeType         string `json:"mime_type"`
	Title            string `json:"title"`
	ID               int    `json:"id"`
}

// page is one page of the list endpoint.
type page struct {
	Next    string     `json:"next"`
	Results []Document `json:"results"`
}

// Client reads one Paperless instance.
type Client struct {
	http  *http.Client
	base  string
	token string
}

// New reads the instance at base with an API token.
func New(httpClient *http.Client, base, token string) *Client {
	return &Client{http: httpClient, base: strings.TrimSuffix(base, "/"), token: token}
}

// List is every document the token's user can see.
func (c *Client) List(ctx context.Context) ([]Document, error) {
	query := url.Values{"page_size": {strconv.Itoa(pageSize)}, "fields": {fields}}
	next := c.base + "/api/documents/?" + query.Encode()
	var documents []Document
	for next != "" {
		var current page
		if err := c.getJSON(ctx, next, &current); err != nil {
			return nil, err
		}
		documents = append(documents, current.Results...)
		next = current.Next
	}
	return documents, nil
}

// Download is a document's file: the original as uploaded, or the archive
// copy Paperless made with a text layer.
func (c *Client) Download(ctx context.Context, id int, original bool) (io.ReadCloser, error) {
	target := fmt.Sprintf("%s/api/documents/%d/download/", c.base, id)
	if original {
		target += "?original=true"
	}
	response, err := c.get(ctx, target)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (c *Client) getJSON(ctx context.Context, target string, into any) error {
	response, err := c.get(ctx, target)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if err := json.NewDecoder(response.Body).Decode(into); err != nil {
		return fmt.Errorf("read %s: %w", target, err)
	}
	return nil
}

// get makes one authenticated request and refuses anything but 200.
func (c *Client) get(ctx context.Context, target string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Token "+c.token)
	request.Header.Set("Accept", "application/json; version="+apiVersion)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("paperless: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("paperless: %s: %s", target, response.Status)
	}
	return response, nil
}
