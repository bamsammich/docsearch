package upload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Client puts files through the authenticated door, for a program holding a
// token.
type Client struct {
	http  *http.Client
	base  string
	token string
}

// NewClient talks to the server at base with token.
func NewClient(httpClient *http.Client, base, token string) *Client {
	return &Client{http: httpClient, base: strings.TrimSuffix(base, "/"), token: token}
}

// Put uploads body as filename under key, replacing whatever key held.
func (c *Client) Put(ctx context.Context, filename, key string, body io.Reader) (Received, error) {
	query := url.Values{"name": {filename}, "key": {key}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.base+"/upload?"+query.Encode(), body)
	if err != nil {
		return Received{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := c.http.Do(request)
	if err != nil {
		return Received{}, fmt.Errorf("upload %s: %w", filename, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusAccepted {
		reason, err := io.ReadAll(io.LimitReader(response.Body, 1<<10))
		return Received{}, errors.Join(fmt.Errorf("upload %s: %s: %s", filename,
			response.Status, strings.TrimSpace(string(reason))), err)
	}
	var received Received
	if err := json.NewDecoder(response.Body).Decode(&received); err != nil {
		return Received{}, fmt.Errorf("upload %s: read the answer: %w", filename, err)
	}
	return received, nil
}
