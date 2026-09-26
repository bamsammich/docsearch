// Package source picks the kind of ingest a target names.
//
// A worker gets a job row holding either a path or a URL, and a ConnectRPC
// caller sends one string. Turning that string into one kind of ingest or the
// other happens once, here.
package source

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bamsammich/docsearch/internal/libroot"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/source/file"
	"github.com/bamsammich/docsearch/internal/source/site"
)

// Registry builds a source from a target.
type Registry struct {
	extractor ingest.Extractor
	// Roots bound which files may be read. A target outside every root is
	// refused, so a caller cannot name a path the operator never offered.
	roots []string
	// CachePath is where a crawl's responses are stored, shared by every
	// site ingest so a re-crawl resumes rather than starting over.
	cachePath string
	site      site.Options
}

// New builds sources that read files under roots through extractor, and
// crawl sites through the cache at cachePath.
func New(
	extractor ingest.Extractor,
	roots []string,
	cachePath string,
	siteOptions site.Options,
) *Registry {
	return &Registry{
		extractor: extractor,
		roots:     roots,
		cachePath: cachePath,
		site:      siteOptions,
	}
}

// For is the source target names. revalidate false re-chunks an
// already-crawled site without a single request, and means nothing to a file.
//
//nolint:ireturn // a factory for a port returns that port; naming a concrete type here would defeat it.
func (r *Registry) For(target string, revalidate bool) (ingest.Source, error) {
	if IsURL(target) {
		if r.cachePath == "" {
			return nil, fmt.Errorf("%s: a site ingest needs a fetch cache", target)
		}
		options := r.site
		options.Revalidate = revalidate
		return site.New(target, r.cachePath, options), nil
	}
	resolved, err := libroot.Resolve(r.roots, target)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", target, err)
	}
	return file.New(resolved, r.extractor), nil
}

// IsURL reports whether a target names a site rather than a path on disk.
func IsURL(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")
}

// Targets is every source one target names, as an identity apiece.
//
// A URL is one site and therefore one document. A directory is still one
// document per file: the site model applies to a crawled site, not to any
// directory that happens to hold Markdown.
func (r *Registry) Targets(target string) ([]string, error) {
	if IsURL(target) {
		source, err := r.For(target, true)
		if err != nil {
			return nil, err
		}
		return []string{source.Identity()}, nil
	}

	resolved, err := libroot.Resolve(r.roots, target)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", target, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", target, err)
	}
	if !info.IsDir() {
		return []string{resolved}, nil
	}

	found, err := r.supportedUnder(resolved)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no supported files under %s", target)
	}
	return found, nil
}

// supportedUnder is every file beneath a directory an adapter can read,
// sorted, so two runs queue the same documents in the same order.
func (r *Registry) supportedUnder(dir string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && r.extractor.Supports(path) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	slices.Sort(found)
	return found, nil
}
