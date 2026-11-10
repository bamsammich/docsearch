package integration

// Copying and moving documents between libraries, through the real worker,
// into a real database, as the restricted role a request uses.

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bamsammich/docsearch/internal/adapter"
	"github.com/bamsammich/docsearch/internal/adapter/pdf"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/sharing"
	"github.com/bamsammich/docsearch/internal/service/worker"
	"github.com/bamsammich/docsearch/internal/site/fetch/pgcache"
	"github.com/bamsammich/docsearch/internal/source"
	"github.com/bamsammich/docsearch/internal/source/site"
	"github.com/bamsammich/docsearch/internal/upload"
	"github.com/bamsammich/docsearch/internal/upload/pgupload"
)

const manual = "# Pager runbook\n\nWhen the pager fires, acknowledge it and open the incident channel.\n"

// shelf is one database with three people and a team library: ana
// administers team, ben edits it and cy reads it.
type shelf struct {
	db   *sql.DB
	root string
	team string
}

func newShelf() *shelf {
	pg := pgtest.Start(GinkgoT())
	ctx := context.Background()
	Expect(schema.Create(ctx, pg.Owner)).To(Succeed())
	for _, account := range []string{"ana", "ben", "cy"} {
		_, err := pg.Owner.ExecContext(
			ctx,
			`SELECT create_account($1, 'https://issuer.example', $1), set_username($1, $1)`,
			account,
		)
		Expect(err).NotTo(HaveOccurred())
	}
	s := &shelf{db: pg.App, root: GinkgoT().TempDir()}
	_, err := postgres.NewLibraries(s.db, "ana").Create(ctx, "team", "Team docs")
	Expect(err).NotTo(HaveOccurred())
	s.join("ben", sharing.Editor)
	s.join("cy", sharing.Reader)
	Expect(pg.Owner.QueryRowContext(ctx,
		`SELECT library_id FROM libraries WHERE slug = 'team'`).Scan(&s.team)).To(Succeed())
	return s
}

func (s *shelf) join(who, role string) {
	ctx := context.Background()
	_, err := postgres.NewLibraries(s.db, "ana").Grant(ctx, "team", who, role)
	Expect(err).NotTo(HaveOccurred())
	_, err = postgres.NewLibraries(s.db, who).Accept(ctx, "team")
	Expect(err).NotTo(HaveOccurred())
}

// uploaded puts manual into who's own library as an upload and ingests it.
func (s *shelf) uploaded(who string) {
	scope := pgsession.Personal(who)
	content := []byte(manual)
	key := upload.Digest(content)
	Expect(pgupload.New(s.db, scope).Put(context.Background(), upload.File{
		Key: key, Filename: "pager.md", SHA256: key, Content: content,
	})).To(Succeed())
	s.queue(scope, upload.Target(key))
	s.work(scope, nil)
}

func (s *shelf) queue(scope pgsession.Scope, target string) {
	_, _, err := postgres.NewJobs(s.db, scope).Add(context.Background(), target, "", false)
	Expect(err).NotTo(HaveOccurred())
}

// work runs the real worker once in scope, which takes one job. guard, when
// set, lets a site on loopback be crawled.
func (s *shelf) work(scope pgsession.Scope, options *site.Options) {
	extractor, err := pdf.New(1)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(extractor.Close()).To(Succeed()) }()
	siteOptions := site.Options{}
	if options != nil {
		siteOptions = *options
	}
	service := worker.New(
		postgres.NewJobs(s.db, scope),
		ingest.New(postgres.New(s.db, scope), time.Now),
		source.New(adapter.New(extractor), []string{s.root},
			pgcache.New(s.db, scope), pgupload.New(s.db, scope), siteOptions),
		worker.Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Once: true},
	)
	Expect(service.Run(context.Background())).To(Succeed())
}

func (s *shelf) start(who string, req document.CopyRequest) (int64, error) {
	return postgres.NewCopies(s.db, who).Start(context.Background(), req)
}

// docs are the ready doc_ids in a library, read as who.
func (s *shelf) docs(who, library string) []string {
	var ids []string
	s.read(who, library, func(tx *sql.Tx) {
		rows, err := tx.Query(`SELECT doc_id FROM documents WHERE status = 'ready' ORDER BY doc_id`)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		for rows.Next() {
			var id string
			Expect(rows.Scan(&id)).To(Succeed())
			ids = append(ids, id)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
	})
	return ids
}

// chunks counts a document's chunks, read as who.
func (s *shelf) chunks(who, library, docID string) int {
	var n int
	s.read(who, library, func(tx *sql.Tx) {
		Expect(tx.QueryRow(`SELECT count(*) FROM chunks WHERE doc_id = $1`, docID).
			Scan(&n)).To(Succeed())
	})
	return n
}

// jobError is a finished job's error, read as who.
func (s *shelf) jobError(who, library string, id int64) string {
	var note sql.NullString
	s.read(who, library, func(tx *sql.Tx) {
		Expect(tx.QueryRow(`SELECT error FROM ingest_jobs WHERE id = $1`, id).
			Scan(&note)).To(Succeed())
	})
	return note.String
}

func (s *shelf) read(who, library string, fn func(tx *sql.Tx)) {
	scope := pgsession.Scope{User: who, Library: library}
	Expect(pgsession.ReadOnly(context.Background(), s.db, scope, func(tx *sql.Tx) error {
		fn(tx)
		return nil
	})).To(Succeed())
}

var _ = Describe("Copying and moving a document between libraries", func() {
	var s *shelf

	BeforeEach(func() {
		s = newShelf()
		s.uploaded("ana")
	})

	It("copies an upload into a library you edit, keeping its doc_id", func() {
		Expect(s.docs("ana", "ana")).To(Equal([]string{"pager-runbook"}))

		_, err := s.start(
			"ana",
			document.CopyRequest{From: "ana", DocID: "pager-runbook", To: s.team},
		)
		Expect(err).NotTo(HaveOccurred())
		s.work(pgsession.Scope{User: "ana", Library: s.team}, nil)

		Expect(s.docs("cy", s.team)).To(Equal([]string{"pager-runbook"}))
		Expect(s.chunks("cy", s.team, "pager-runbook")).To(BeNumerically(">", 0))
		Expect(
			s.docs("ana", "ana"),
		).To(Equal([]string{"pager-runbook"}), "a copy leaves the original")
	})

	It("lets a reader copy out but not move out", func() {
		s.copyToTeam()

		_, err := s.start("cy", document.CopyRequest{
			From: s.team, DocID: "pager-runbook", To: "cy", Move: true,
		})
		Expect(err).To(MatchError(document.ErrCannotMove))

		_, err = s.start("cy", document.CopyRequest{From: s.team, DocID: "pager-runbook", To: "cy"})
		Expect(err).NotTo(HaveOccurred())
		s.work(pgsession.Personal("cy"), nil)
		Expect(s.docs("cy", "cy")).To(Equal([]string{"pager-runbook"}))
	})

	It(
		"refuses a library you only read, and a document you cannot read, alike to strangers",
		func() {
			_, err := s.start("cy", document.CopyRequest{From: "cy", DocID: "nothing", To: s.team})
			Expect(err).To(MatchError(ContainSubstring("no such document")))

			_, err = s.start(
				"ana",
				document.CopyRequest{From: "ana", DocID: "pager-runbook", To: "cy"},
			)
			Expect(err).To(MatchError(document.ErrCannotAdd))

			_, err = s.start(
				"ben",
				document.CopyRequest{From: "ana", DocID: "pager-runbook", To: s.team},
			)
			Expect(err).To(MatchError(ContainSubstring("no such document")),
				"ana's library answers ben as if the document were not there")
		},
	)

	It("refuses a copy the target already holds", func() {
		s.copyToTeam()

		_, err := s.start(
			"ana",
			document.CopyRequest{From: "ana", DocID: "pager-runbook", To: s.team},
		)
		var held document.AlreadyHeld
		Expect(errors.As(err, &held)).To(BeTrue(), "got %v", err)
		Expect(held.DocID).To(Equal("pager-runbook"))
	})

	It(
		"refuses a doc_id the target files another document under, until --as names another",
		func() {
			other := filepath.Join(s.root, "other.md")
			Expect(
				os.WriteFile(other, []byte("# Pager runbook\n\nA different text.\n"), 0o600),
			).To(Succeed())
			s.queue(pgsession.Scope{User: "ana", Library: s.team}, other)
			s.work(pgsession.Scope{User: "ana", Library: s.team}, nil)
			Expect(s.docs("ana", s.team)).To(Equal([]string{"pager-runbook"}))

			_, err := s.start(
				"ana",
				document.CopyRequest{From: "ana", DocID: "pager-runbook", To: s.team},
			)
			Expect(err).To(MatchError(document.Taken{DocID: "pager-runbook"}))

			_, err = s.start("ana", document.CopyRequest{
				From: "ana", DocID: "pager-runbook", To: s.team, As: "pager-runbook-ana",
			})
			Expect(err).NotTo(HaveOccurred())
			s.work(pgsession.Scope{User: "ana", Library: s.team}, nil)
			Expect(s.docs("ana", s.team)).To(Equal([]string{"pager-runbook", "pager-runbook-ana"}))
		},
	)

	It("copies a file by reading the same path in the new library", func() {
		path := filepath.Join(s.root, "guide.md")
		Expect(
			os.WriteFile(path, []byte("# Deploy guide\n\nShip on Tuesdays.\n"), 0o600),
		).To(Succeed())
		s.queue(pgsession.Personal("ana"), path)
		s.work(pgsession.Personal("ana"), nil)

		_, err := s.start(
			"ana",
			document.CopyRequest{From: "ana", DocID: "deploy-guide", To: s.team},
		)
		Expect(err).NotTo(HaveOccurred())
		s.work(pgsession.Scope{User: "ana", Library: s.team}, nil)
		Expect(s.docs("ben", s.team)).To(ContainElement("deploy-guide"))
	})

	It("moves a document once its copy is ready", func() {
		s.copyToTeam()

		_, err := s.start("ben", document.CopyRequest{
			From: s.team, DocID: "pager-runbook", To: "ben", Move: true,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.docs("ben", s.team)).To(ContainElement("pager-runbook"),
			"the original stays until the copy is ready")

		s.work(pgsession.Personal("ben"), nil)
		Expect(s.docs("ben", "ben")).To(Equal([]string{"pager-runbook"}))
		Expect(s.docs("ben", s.team)).To(BeEmpty())
	})

	It("keeps the original when the mover stopped editing it while the copy waited", func() {
		s.copyToTeam()
		job, err := s.start("ben", document.CopyRequest{
			From: s.team, DocID: "pager-runbook", To: "ben", Move: true,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = postgres.NewLibraries(s.db, "ana").
			Grant(context.Background(), "team", "ben", sharing.Reader)
		Expect(err).NotTo(HaveOccurred())

		s.work(pgsession.Personal("ben"), nil)
		Expect(s.docs("ben", "ben")).To(Equal([]string{"pager-runbook"}))
		Expect(s.docs("ben", s.team)).To(Equal([]string{"pager-runbook"}))
		Expect(s.jobError("ben", "ben", job)).To(ContainSubstring("original stayed"))
	})

	It("copies a crawled site with its cache, so the new library sends no request", func() {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			if r.URL.Path == "/robots.txt" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			page := `<html><head><title>Widget handbook</title></head>` +
				`<body><main><h1>Widget handbook</h1><p>Widgets turn clockwise.</p></main></body></html>`
			if _, err := io.WriteString(w, page); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}))
		options := &site.Options{Guard: loopbackGuard, Interval: time.Millisecond}
		s.queue(pgsession.Personal("ana"), server.URL+"/docs/")
		s.work(pgsession.Personal("ana"), options)
		server.Close()
		Expect(requests).To(BeNumerically(">", 0))
		docs := s.docs("ana", "ana")
		Expect(docs).To(ContainElement("widget-handbook"))

		_, err := s.start(
			"ana",
			document.CopyRequest{From: "ana", DocID: "widget-handbook", To: s.team},
		)
		Expect(err).NotTo(HaveOccurred())
		s.work(pgsession.Scope{User: "ana", Library: s.team}, options)

		Expect(s.docs("cy", s.team)).To(ContainElement("widget-handbook"),
			"re-indexed from the copied cache with the site gone")
		Expect(s.chunks("cy", s.team, "widget-handbook")).To(BeNumerically(">", 0))
	})
})

// copyToTeam copies ana's uploaded manual into team and runs its job.
func (s *shelf) copyToTeam() {
	_, err := s.start("ana", document.CopyRequest{From: "ana", DocID: "pager-runbook", To: s.team})
	Expect(err).NotTo(HaveOccurred())
	s.work(pgsession.Scope{User: "ana", Library: s.team}, nil)
	Expect(s.docs("ana", s.team)).To(Equal([]string{"pager-runbook"}))
}
