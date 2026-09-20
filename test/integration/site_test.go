package integration

// Site parity: the Go crawl and site extraction against the Python one.
//
// Each fixture under testdata/site is a documentation site this project
// invents, together with the Python pipeline's crawl of it. Probing a real
// site would make the reference depend on a stranger's uptime and on what
// they publish this week, so the site is served here from the same route
// manifest scripts/site_goldens.py served when it wrote the golden.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/site"
	"github.com/bamsammich/docsearch/internal/site/crawl"
	"github.com/bamsammich/docsearch/internal/site/fetch"
	"github.com/bamsammich/docsearch/internal/urlguard"
)

// originToken stands in for the server's origin in a fixture and in a
// golden. The port is chosen at run time, so neither the sitemap the fixture
// serves nor the URLs the crawl records can be written down.
const originToken = "{{ORIGIN}}"

// route is one address the fixture answers at.
type route struct {
	File   string `json:"file"`
	Status int    `json:"status"`
}

// manifest is a fixture's site.json: every address it answers at, and what
// it answers for everything else.
type manifest struct {
	Routes   map[string]route `json:"routes"`
	Seed     string           `json:"seed"`
	NotFound route            `json:"not_found"`
}

// contentTypes is how both servers name what they serve. Written down rather
// than guessed, so a static file server's opinion cannot become a difference
// between the two pipelines.
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".txt":  "text/plain; charset=utf-8",
	".xml":  "application/xml",
}

var _ = Describe("Site parity with the Python pipeline", func() {
	for _, name := range []string{"declared", "walked"} {
		Context(name, func() {
			var (
				dir    string
				server *httptest.Server
				ext    *domain.Extraction
			)

			BeforeEach(func() {
				root, err := repoRoot()
				Expect(err).NotTo(HaveOccurred())
				dir = filepath.Join(root, "testdata", "site", name)

				var m manifest
				Expect(readJSON(filepath.Join(dir, "site.json"), &m)).To(Succeed())
				server = httptest.NewServer(fixtureHandler(filepath.Join(dir, "pages"), m))
				DeferCleanup(server.Close)

				ext, err = crawlFixture(server.URL + m.Seed)
				Expect(err).NotTo(HaveOccurred())
			})

			It("extracts what the Python pipeline extracts", func() {
				var want domain.Extraction
				Expect(readGolden(dir, "extraction.golden.json", server.URL, &want)).To(Succeed())
				Expect(extractionDifference(ext, &want)).To(BeEmpty())
			})

			It("cuts the same chunks", func() {
				var want []domain.Chunk
				Expect(readGolden(dir, "chunks.golden.json", server.URL, &want)).To(Succeed())
				Expect(firstDifference(domain.Chunks(*ext), want)).To(BeEmpty())
			})
		})
	}
})

// crawlFixture crawls a served fixture and builds its extraction.
func crawlFixture(seed string) (*domain.Extraction, error) {
	cache, err := fetch.OpenSQLiteCache(
		context.Background(),
		filepath.Join(GinkgoT().TempDir(), "cache.db"),
	)
	if err != nil {
		return nil, err
	}
	defer func() { Expect(cache.Close()).To(Succeed()) }()

	fetcher := fetch.New(cache, fetch.Options{Guard: loopbackGuard, Interval: time.Millisecond})
	result, err := crawl.Crawl(context.Background(), fetcher, seed, crawl.Options{Revalidate: true})
	if err != nil {
		return nil, err
	}
	return site.BuildExtraction(result, "")
}

// loopbackGuard approves the fixture server. The real guard refuses
// loopback, which is exactly what a fixture server is; its own tests cover
// that refusal.
func loopbackGuard(_ context.Context, raw string) (*urlguard.Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", raw, err)
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil {
		return nil, fmt.Errorf("address of %s: %w", raw, err)
	}
	return &urlguard.Target{URL: u, Addrs: []netip.Addr{addr}}, nil
}

// fixtureHandler serves exactly what the manifest declares, as the Python
// generator's own server does.
func fixtureHandler(pages string, m manifest) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		matched, ok := m.Routes[r.URL.Path]
		if !ok {
			matched = m.NotFound
		}
		body, err := os.ReadFile(filepath.Join(pages, matched.File))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		origin := "http://" + r.Host
		body = []byte(strings.ReplaceAll(string(body), originToken, origin))
		w.Header().Set("Content-Type", contentTypes[filepath.Ext(matched.File)])
		w.WriteHeader(statusOr(matched.Status, http.StatusOK))
		if _, err := w.Write(body); err != nil {
			GinkgoWriter.Printf("serving %s: %v\n", r.URL.Path, err)
		}
	})
}

func statusOr(status, fallback int) int {
	if status == 0 {
		return fallback
	}
	return status
}

// readGolden decodes a golden, naming the server this run happens to have
// started wherever the golden holds the origin token.
func readGolden(dir, name, origin string, v any) error {
	path := filepath.Join(dir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	resolved := strings.ReplaceAll(string(raw), originToken, origin)
	if err := json.Unmarshal([]byte(resolved), v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
