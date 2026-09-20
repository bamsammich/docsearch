package integration

// The whole pipeline, in Go, end to end.
//
// Every other suite holds one package to its own contract. This one runs the
// worker over real files, into a real database, and then reads that database
// back through internal/store, which is what the MCP server reads with. A
// document that every unit test approves of is still worthless if it cannot
// be searched, and only running both halves shows that.
//
// The Python verifier grades the result too, while it is still the reference.
// Step 7 ports it, and this spec then holds the Go one.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/repository/sqlite"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/worker"
	"github.com/bamsammich/docsearch/internal/source"
	"github.com/bamsammich/docsearch/internal/source/site"
	"github.com/bamsammich/docsearch/internal/store"
)

// index is one database, built by the worker and read back by the server's
// own store.
type index struct {
	db    *sql.DB
	store *store.Store
	root  string
	path  string
}

var _ = Describe("An index built by Go", Ordered, func() {
	// One fixture per adapter, so a format that reached the database wrong
	// fails here rather than in the format's own suite.
	fixtures := []string{"guide.md", "page.html", "styled.docx", "numbered.pdf"}
	var built *index

	BeforeAll(func() {
		built = newIndex()
		for _, fixture := range fixtures {
			built.copyFixture(fixture)
		}
		built.work(len(fixtures))
	})

	It("holds every document as ready and searchable", func() {
		listed, err := built.store.ListDocuments(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(len(fixtures)))
		for _, doc := range listed {
			Expect(doc.Quality).NotTo(Equal("failed"), doc.DocID)
			Expect(doc.ChunkCount).NotTo(BeNil(), doc.DocID)
			Expect(*doc.ChunkCount).To(BeNumerically(">", 0), doc.DocID)
		}
	})

	It("answers a search with the text that was ingested", func() {
		results, err := built.store.Search(context.Background(), store.SearchParams{
			Query: "executors", K: 5,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(results).NotTo(BeEmpty(),
			"the markdown fixture's body should be reachable through the full-text index")
	})

	It("describes a document's headings", func() {
		docID := built.docIDFor("guide.md")
		outline, err := built.store.Outline(context.Background(), docID, 3)
		Expect(err).NotTo(HaveOccurred())
		Expect(outline).NotTo(BeEmpty())
		Expect(outline[0].Heading).NotTo(BeEmpty())
	})

	It("numbers every document's chunks without a gap", func() {
		// A gap means a batch was lost between transactions, which every
		// read path would then step over silently.
		rows, err := built.db.Query(
			`SELECT doc_id, COUNT(*), MIN(ordinal), MAX(ordinal)
			   FROM chunks GROUP BY doc_id`)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		for rows.Next() {
			var docID string
			var count, lowest, highest int
			Expect(rows.Scan(&docID, &count, &lowest, &highest)).To(Succeed())
			Expect(lowest).To(Equal(0), docID)
			Expect(highest).To(Equal(count-1), docID)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
	})

	It("passes the Python verifier", func() {
		// The reference until step 7 ports it. A non-zero exit is a database
		// integrity problem, which is what this spec is for; the chunk-
		// quality verdict is the structure policy's business and is graded
		// at ingest.
		if _, err := exec.LookPath("uv"); err != nil {
			Skip("uv is not installed, so the Python reference cannot be run")
		}
		for _, fixture := range fixtures {
			docID := built.docIDFor(fixture)
			out, err := built.verify(docID)
			Expect(err).NotTo(HaveOccurred(), "%s:\n%s", docID, out)
		}
	})

	It("refuses a document that indexes to nothing, and keeps nothing", func() {
		// Extraction found a file it could read and no text survived, which
		// is a defect worth surfacing rather than a ready document no query
		// can ever return.
		empty := built.write("empty.md", "")
		jobID := built.enqueue(empty)
		built.work(1)

		Expect(built.jobField(jobID, "status")).To(Equal("failed"))
		Expect(built.jobField(jobID, "permanent")).To(Equal("1"),
			"the same bytes would be refused again, so retrying buys nothing")
		Expect(built.jobField(jobID, "error")).To(ContainSubstring("no chunks"))
		Expect(built.count(`SELECT COUNT(*) FROM documents WHERE source_path=?`, empty)).
			To(Equal(0))
	})

	It("refuses a format no adapter reads, without hashing it", func() {
		archive := built.write("archive.zip", "not a document")
		jobID := built.enqueue(archive)
		built.work(1)

		Expect(built.jobField(jobID, "status")).To(Equal("failed"))
		Expect(built.jobField(jobID, "permanent")).To(Equal("1"))
	})

	It("re-reads a changed document in place", func() {
		// Replacement is keyed on where the document was read from, so a
		// retitled file must replace its own rows rather than become a
		// second document.
		path := built.write("changing.md", "# First Title\n\nA paragraph about the console.\n")
		built.enqueue(path)
		built.work(1)
		first := built.docIDFor("changing.md")

		Expect(os.WriteFile(path,
			[]byte("# Second Title\n\nA different paragraph about the console.\n"),
			0o600)).To(Succeed())
		built.enqueue(path)
		built.work(1)

		Expect(built.count(`SELECT COUNT(*) FROM documents WHERE source_path=?`, path)).
			To(Equal(1), "one document, not two")
		Expect(built.docIDFor("changing.md")).To(Equal(first),
			"the identifier it already had")
	})

	It("does nothing for bytes it already holds", func() {
		original := built.write("original.md", "# Shared\n\nThe very same words, twice over.\n")
		built.enqueue(original)
		built.work(1)

		duplicate := built.write("duplicate.md", "# Shared\n\nThe very same words, twice over.\n")
		jobID := built.enqueue(duplicate)
		built.work(1)

		Expect(built.jobField(jobID, "status")).To(Equal("done"))
		Expect(built.count(`SELECT COUNT(*) FROM documents WHERE source_path=?`, duplicate)).
			To(Equal(0), "the second file is not a second document")
	})
})

// newIndex builds an empty index with a library root beside it.
func newIndex() *index {
	root, err := repoRoot()
	Expect(err).NotTo(HaveOccurred())

	dir := GinkgoT().TempDir()
	library := filepath.Join(dir, "library")
	Expect(os.Mkdir(library, 0o700)).To(Succeed())

	path := filepath.Join(dir, "index.db")
	db, err := sqlite.Open(path)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

	schema, err := os.ReadFile(filepath.Join(root, "python", "docsearch", "schema.sql"))
	Expect(err).NotTo(HaveOccurred())
	_, err = db.Exec(string(schema))
	Expect(err).NotTo(HaveOccurred())

	// schema.sql creates the version table and leaves it empty, so the stamp
	// is written here the way db.connect writes it. Creating an index is
	// still Python's job; step 7 gives the Go CLI a migrate of its own, and
	// until then an unstamped database is refused by every Python command.
	_, err = db.Exec(
		`INSERT INTO schema_version (version, applied_at) VALUES (?, datetime('now'))`,
		store.RequiredSchemaVersion)
	Expect(err).NotTo(HaveOccurred())

	reader, err := store.Open(path)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(reader.Close()).To(Succeed()) })

	return &index{db: db, store: reader, root: library, path: path}
}

// copyFixture puts a committed adapter fixture in the library and queues it.
func (i *index) copyFixture(name string) {
	root, err := repoRoot()
	Expect(err).NotTo(HaveOccurred())
	body, err := os.ReadFile(filepath.Join(root, "testdata", "adapters", name))
	Expect(err).NotTo(HaveOccurred())
	i.enqueue(i.writeBytes(name, body))
}

func (i *index) write(name, body string) string {
	return i.writeBytes(name, []byte(body))
}

func (i *index) writeBytes(name string, body []byte) string {
	path := filepath.Join(i.root, name)
	Expect(os.WriteFile(path, body, 0o600)).To(Succeed())
	resolved, err := filepath.EvalSymlinks(path)
	Expect(err).NotTo(HaveOccurred())
	return resolved
}

// enqueue puts one job on the queue, as the CLI's add does.
func (i *index) enqueue(path string) int64 {
	res, err := i.db.Exec(
		`INSERT INTO ingest_jobs (source_path, status, created_at, updated_at)
		 VALUES (?, 'queued', datetime('now'), datetime('now'))`, path)
	Expect(err).NotTo(HaveOccurred())
	id, err := res.LastInsertId()
	Expect(err).NotTo(HaveOccurred())
	return id
}

// work runs the real worker until it has taken jobs times from the queue.
func (i *index) work(jobs int) {
	extractor, err := pdf.New()
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(extractor.Close()).To(Succeed()) }()

	service := worker.New(
		sqlite.NewJobs(i.db),
		ingest.New(sqlite.New(i.db), time.Now),
		source.New(adapter.New(extractor), []string{i.root}, i.cachePath(), site.Options{}),
		// The worker logs each job; the suite's own output is the report.
		worker.Options{
			Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Once: true,
		},
	)
	for range jobs {
		Expect(service.Run(context.Background())).To(Succeed())
	}
}

func (i *index) cachePath() string {
	return filepath.Join(filepath.Dir(i.path), "fetch-cache.db")
}

// docIDFor is the identifier the document read from name was filed under.
func (i *index) docIDFor(name string) string {
	var docID string
	err := i.db.QueryRow(
		`SELECT doc_id FROM documents WHERE source_path LIKE ?`, "%/"+name).Scan(&docID)
	Expect(err).NotTo(HaveOccurred(), name)
	return docID
}

func (i *index) jobField(id int64, column string) string {
	var value sql.NullString
	err := i.db.QueryRow(
		fmt.Sprintf(`SELECT %s FROM ingest_jobs WHERE id=?`, column), id).Scan(&value)
	Expect(err).NotTo(HaveOccurred())
	return value.String
}

func (i *index) count(query string, args ...any) int {
	var n int
	Expect(i.db.QueryRow(query, args...).Scan(&n)).To(Succeed())
	return n
}

// verify runs the Python verifier over this index.
func (i *index) verify(docID string) (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	cmd := exec.Command("uv", "run", "docsearch", "verify", docID, "--db", i.path)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return string(out), err
}
