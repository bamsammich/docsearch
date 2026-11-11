// Package userinfo reads the claims a provider states about the holder of an
// access token, from its userinfo endpoint as OpenID Connect Core 5.3
// defines it.
//
// Some providers put a claim, such as the groups a person is in, only in the
// ID token and the userinfo response, never in the access token. docsearch
// receives access tokens alone, so this is where it reads such a claim.
package userinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/zitadel/oidc/v3/pkg/client"
)

// requestTimeout bounds one userinfo request. The request is made while a
// person waits for a page, so a provider that hangs fails it rather than
// holding the page open.
const requestTimeout = 10 * time.Second

// maxResponse is the most of a userinfo response read. A real one is a few
// kilobytes even for a person in many groups.
const maxResponse = 1 << 20

// ErrSubject is a userinfo response about someone other than the token's
// subject, which OpenID Connect Core 5.3.2 says must not be used.
var ErrSubject = errors.New("the userinfo response names a different subject")

// Reader asks one provider's userinfo endpoint.
type Reader struct {
	http     *http.Client
	endpoint string
}

// Discover finds the issuer's userinfo endpoint.
func Discover(ctx context.Context, issuer string) (*Reader, error) {
	found, err := client.Discover(ctx, issuer, http.DefaultClient)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", issuer, err)
	}
	if found.UserinfoEndpoint == "" {
		return nil, fmt.Errorf("%s publishes no userinfo endpoint", issuer)
	}
	return New(found.UserinfoEndpoint, http.DefaultClient), nil
}

// New reads claims from endpoint through httpClient.
func New(endpoint string, httpClient *http.Client) *Reader {
	return &Reader{http: httpClient, endpoint: endpoint}
}

// Claims are what the provider states about the holder of token, who must
// be subject.
func (r *Reader) Claims(ctx context.Context, token, subject string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo answered %s", resp.Status)
	}
	claims := map[string]any{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&claims); err != nil {
		return nil, fmt.Errorf("userinfo response: %w", err)
	}
	if stated, ok := claims["sub"].(string); !ok || stated != subject {
		return nil, ErrSubject
	}
	return claims, nil
}
