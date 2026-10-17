// Package upload is how a file reaches docsearch when no path to it does.
//
// A tool call cannot carry a file: its arguments are generated a token at a
// time, so a 10MB PDF never fits. A file comes in by a side door instead, and
// there are two. A signed link is for a person: an agent asks for one, the
// person opens it and picks a file, and the link is the only credential the
// browser needs. An authenticated PUT is for a program, such as a sync, that
// already holds a token.
//
// Both doors end the same way. The bytes are stored in the uploader's own
// rows, and the file is queued as source `upload:<key>` for the worker, which
// reads it back from the database. The key is what replaces what: a client
// that names one gets its document replaced in place on the next upload, and
// one that does not gets the content hash.
package upload

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// scheme prefixes the ingest target of an uploaded file.
const scheme = "upload:"

// ErrNotFound is a key the user never uploaded.
var ErrNotFound = errors.New("no upload under that key")

// File is one uploaded file.
type File struct {
	Key      string
	Filename string
	SHA256   string
	Content  []byte
}

// Target is the ingest target an uploaded file is queued as.
func Target(key string) string { return scheme + key }

// KeyOf is the key in an upload target, and whether target is one.
func KeyOf(target string) (string, bool) {
	key, ok := strings.CutPrefix(target, scheme)
	return key, ok && key != ""
}

// Digest is a file's SHA-256, the same digest a file read from disk gets.
func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// Store holds one user's uploads.
type Store interface {
	Put(ctx context.Context, file File) error
	// Get is the file under key, or ErrNotFound.
	Get(ctx context.Context, key string) (File, error)
}

// Links keeps signed links by the hash of their token, never the token.
type Links interface {
	Create(ctx context.Context, user, tokenHash string, expires time.Time) error
	// Redeem spends a link and names its user. A link already spent, expired
	// or never issued reports false.
	Redeem(ctx context.Context, tokenHash string) (string, bool, error)
	// Open reports whether a link would still redeem, without spending it.
	Open(ctx context.Context, tokenHash string) (bool, error)
}

// Link is a signed link, as handed to whoever asked for it.
type Link struct {
	Expires time.Time
	URL     string
}

// Issuer hands out signed links.
type Issuer struct {
	links Links
	now   func() time.Time
	base  string
	ttl   time.Duration
}

// NewIssuer issues links under base, the URL a person's browser reaches
// docsearch at, each good for ttl.
func NewIssuer(links Links, base string, ttl time.Duration, now func() time.Time) *Issuer {
	return &Issuer{links: links, base: strings.TrimSuffix(base, "/"), ttl: ttl, now: now}
}

// Issue makes a link that uploads one file into user's library.
func (i *Issuer) Issue(ctx context.Context, user string) (Link, error) {
	token := rand.Text()
	expires := i.now().Add(i.ttl)
	if err := i.links.Create(ctx, user, hashToken(token), expires); err != nil {
		return Link{}, err
	}
	return Link{URL: i.base + LinkPrefix + token, Expires: expires}, nil
}

// LinkPrefix is the path signed links are served under.
const LinkPrefix = "/upload/"

// hashToken is what the database holds in place of a token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
