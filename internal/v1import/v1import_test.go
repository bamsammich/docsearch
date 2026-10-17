package v1import_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/service/job"
	"github.com/bamsammich/docsearch/internal/v1import"
	"github.com/bamsammich/docsearch/internal/v1import/mocks"
)

// v1Schema is the documents table as v1 last shipped it. The import reads
// three columns; the rest are here so the fixture is a v1 file, not a
// table shaped to suit the reader.
const v1Schema = `CREATE TABLE documents (
  doc_id TEXT PRIMARY KEY, title TEXT NOT NULL, format TEXT NOT NULL,
  source_path TEXT NOT NULL, source_kind TEXT NOT NULL DEFAULT 'file',
  sha256 TEXT NOT NULL, page_count INTEGER, chunk_count INTEGER,
  status TEXT NOT NULL, ingested_at TEXT, warnings TEXT)`

type ImportSuite struct {
	suite.Suite
}

func TestImport(t *testing.T) { suite.Run(t, new(ImportSuite)) }

// v1File writes a v1 index holding documents and returns its path.
func (s *ImportSuite) v1File(documents ...v1import.Document) string {
	path := filepath.Join(s.T().TempDir(), "docsearch.db")
	db, err := sql.Open("sqlite", path)
	s.Require().NoError(err)
	defer func() { s.Require().NoError(db.Close()) }()
	_, err = db.ExecContext(s.T().Context(), v1Schema)
	s.Require().NoError(err)
	for i, d := range documents {
		_, err = db.ExecContext(s.T().Context(),
			`INSERT INTO documents (doc_id, title, format, source_path, sha256, status)
			 VALUES (?, ?, 'pdf', ?, 'abc', ?)`, i, d.Title, d.Source, d.Status)
		s.Require().NoError(err)
	}
	return path
}

func (s *ImportSuite) TestReadsEveryDocumentV1Held() {
	held := []v1import.Document{
		{Source: "/library/a.pdf", Title: "A manual", Status: "ready"},
		{Source: "https://docs.example.com/", Title: "A site", Status: "ready"},
		{Source: "/library/broken.pdf", Title: "Broken", Status: "failed"},
	}
	got, err := v1import.Read(s.T().Context(), s.v1File(held...))
	s.Require().NoError(err)
	s.ElementsMatch(held, got)
}

// Read-only means a path that is not there is an error, not a new empty
// index that reports nothing to import.
func (s *ImportSuite) TestAMissingFileIsAnErrorNotAnEmptyLibrary() {
	missing := filepath.Join(s.T().TempDir(), "absent.db")
	_, err := v1import.Read(s.T().Context(), missing)
	s.Require().Error(err)
	_, statErr := os.Stat(missing)
	s.True(os.IsNotExist(statErr), "reading must not create the file")
}

func (s *ImportSuite) TestPlanMovesFilesAndLeavesSitesAlone() {
	roots := []v1import.RootMap{{From: "/library", To: "/srv/docs"}}
	got := v1import.Plan([]v1import.Document{
		{Source: "/library/manuals/a.pdf", Title: "A", Status: "ready"},
		{Source: "https://docs.example.com/", Title: "Site", Status: "ready"},
		{Source: "/elsewhere/b.pdf", Title: "B", Status: "ready"},
		{Source: "/library-two/c.pdf", Title: "C", Status: "ready"},
		{Source: "/library/d.pdf", Title: "D", Status: "failed"},
	}, roots)

	s.Equal([]v1import.Entry{
		{Source: "/srv/docs/manuals/a.pdf", Title: "A"},
		{Source: "https://docs.example.com/", Title: "Site"},
		// Under no mapped root: passed through for v2 to accept or refuse.
		{Source: "/elsewhere/b.pdf", Title: "B"},
		// A sibling whose name starts the same is not under the root.
		{Source: "/library-two/c.pdf", Title: "C"},
		{Source: "/srv/docs/d.pdf", Title: "D", Skipped: "v1 left it failed"},
	}, got)
}

func (s *ImportSuite) TestARootMapNeedsBothSides() {
	for _, spelling := range []string{"/library", "=/srv", "/library=", ""} {
		_, err := v1import.ParseRootMap(spelling)
		s.Require().Error(err, spelling)
	}
	got, err := v1import.ParseRootMap("/library/=/srv/docs")
	s.Require().NoError(err)
	s.Equal(v1import.RootMap{From: "/library", To: "/srv/docs"}, got)
}

// One refused source does not stop the import, and a skipped one is never
// sent.
func (s *ImportSuite) TestRunQueuesTheRestPastARefusal() {
	enqueuer := mocks.NewMockEnqueuer(s.T())
	enqueuer.EXPECT().Enqueue(mock.Anything, "/srv/a.pdf", "A").
		Return(nil, errors.New("outside the library root"))
	enqueuer.EXPECT().Enqueue(mock.Anything, "/srv/b.pdf", "B").
		Return([]job.Queued{{JobID: 7, Source: "/srv/b.pdf"}}, nil)

	got := v1import.Run(s.T().Context(), enqueuer, []v1import.Entry{
		{Source: "/srv/a.pdf", Title: "A"},
		{Source: "/srv/b.pdf", Title: "B"},
		{Source: "/srv/c.pdf", Title: "C", Skipped: "v1 left it failed"},
	})

	s.Require().Len(got, 3)
	s.Require().Error(got[0].Err)
	s.Equal(int64(7), got[1].Jobs[0].JobID)
	s.Require().NoError(got[2].Err)
	s.Empty(got[2].Jobs)
}
