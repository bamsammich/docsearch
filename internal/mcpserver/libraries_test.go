package mcpserver

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgstore"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

// LibrariesSuite drives the read tools against Postgres across two
// libraries: the built-in account's own, and a shared one it administers
// and a second account reads. Both libraries hold a document called guide,
// which is what makes naming a document by its library necessary.
type LibrariesSuite struct {
	suite.Suite
	pg *pgtest.DB
	d  Deps
}

func TestLibraries(t *testing.T) { suite.Run(t, new(LibrariesSuite)) }

func (s *LibrariesSuite) SetupTest() {
	s.pg = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.pg.Owner))
	for _, statement := range []string{
		`SELECT create_account('second', 'https://issuer.example', 'subject-2')`,
		`INSERT INTO libraries (library_id, kind, slug, name)
		   VALUES ('lib_team', 'shared', 'team', 'Team docs')`,
		`CREATE TABLE chunks_lib_team PARTITION OF chunks FOR VALUES IN ('lib_team')`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON chunks_lib_team TO docsearch_app`,
		`INSERT INTO library_grants (library_id, user_id, role)
		   VALUES ('lib_team', 'default', 'admin'), ('lib_team', 'second', 'reader')`,
	} {
		_, err := s.pg.Owner.ExecContext(s.T().Context(), statement)
		s.Require().NoError(err)
	}
	s.document(pgsession.Personal("default"), "guide", "Install > Unpack",
		"unpack the archive before the installer runs")
	s.document(pgsession.Scope{User: "default", Library: "lib_team"}, "guide",
		"Install > Archive", "the team keeps the archive on the shared drive")
	s.document(pgsession.Scope{User: "default", Library: "lib_team"}, "runbook",
		"Restore > Archive", "restore the archive from last night")
	s.document(pgsession.Personal("second"), "private-notes", "Notes",
		"the archive password is not here")

	s.d = Deps{
		Log:    slog.New(slog.DiscardHandler),
		Stores: func(scope pgsession.Scope) (*pgstore.Store, error) { return pgstore.New(s.pg.App, scope), nil },
	}
}

// document writes one ready document of one chunk into scope's library.
func (s *LibrariesSuite) document(scope pgsession.Scope, docID, heading, text string) {
	ctx := s.T().Context()
	tx, err := s.pg.Owner.BeginTx(ctx, nil)
	s.Require().NoError(err)
	defer func() { s.NoError(pgsession.Rollback(tx)) }()
	s.Require().NoError(pgsession.Rescope(ctx, tx, scope))
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO documents (library_id, doc_id, title, format, source_path, sha256, status, chunk_count)
		 VALUES ($1, $2, $2, 'markdown', '/x/' || $2, md5($1 || $2), 'ready', 1)`,
		scope.Library,
		docID,
	)
	s.Require().NoError(err)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO chunks (library_id, doc_id, ordinal, section, heading_path, text)
		 VALUES ($1, $2, 0, '1', $3, $4)`,
		scope.Library, docID, heading, text)
	s.Require().NoError(err)
	s.Require().NoError(tx.Commit())
}

func as(user string) context.Context { return owner.With(context.Background(), user) }

func docIDs(docs []pgstore.Document) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.DocID)
	}
	return out
}

func resultIDs(results []pgstore.SearchResult) []string {
	var out []string
	for _, r := range results {
		if !slices.Contains(out, r.DocID) {
			out = append(out, r.DocID)
		}
	}
	slices.Sort(out)
	return out
}

func (s *LibrariesSuite) TestListDocumentsCoversEveryLibraryTheUserReads() {
	_, out, err := s.d.listDocuments(as("default"), nil, listDocumentsInput{})
	s.Require().NoError(err)

	s.Equal([]string{"guide", "team/guide", "team/runbook"}, docIDs(out.Documents))
	s.Require().Len(out.Libraries, 2)
	s.Equal(libraryEntry{Library: out.Libraries[0].Library, Documents: 1}, out.Libraries[0])
	s.Equal("personal", out.Libraries[0].Label)
	s.Equal("admin", out.Libraries[0].Role)
	s.Equal("team", out.Libraries[1].Label)
	s.Equal("Team docs", out.Libraries[1].Name)
	s.Equal(2, out.Libraries[1].Documents)
}

func (s *LibrariesSuite) TestAReaderListsTheSharedLibraryAndNotAnotherPersonalOne() {
	_, out, err := s.d.listDocuments(as("second"), nil, listDocumentsInput{})
	s.Require().NoError(err)

	s.Equal([]string{"private-notes", "team/guide", "team/runbook"}, docIDs(out.Documents))
	s.Equal("reader", out.Libraries[1].Role)
}

func (s *LibrariesSuite) TestLibrariesNarrowsTheList() {
	_, out, err := s.d.listDocuments(
		as("default"),
		nil,
		listDocumentsInput{Libraries: []string{"team"}},
	)
	s.Require().NoError(err)
	s.Equal([]string{"team/guide", "team/runbook"}, docIDs(out.Documents))

	_, _, err = s.d.listDocuments(
		as("default"),
		nil,
		listDocumentsInput{Libraries: []string{"nope"}},
	)
	s.Require().ErrorContains(err, `you read no library named "nope"`)
}

func (s *LibrariesSuite) TestAnUnscopedSearchCoversEveryLibraryTheUserReads() {
	_, out, err := s.d.search(as("default"), nil, searchInput{Query: "archive", K: 10})
	s.Require().NoError(err)

	s.Equal([]string{"guide", "team/guide", "team/runbook"}, resultIDs(out.Results))
	for _, r := range out.Results {
		if r.DocID == "guide" {
			s.Equal("personal", r.Library)
		} else {
			s.Equal("team", r.Library)
		}
	}
	for i, r := range out.Results {
		s.Equal(i+1, r.Rank)
	}
}

func (s *LibrariesSuite) TestASearchNeverReachesALibraryWithoutAGrant() {
	_, out, err := s.d.search(as("second"), nil, searchInput{Query: "archive", K: 10})
	s.Require().NoError(err)
	s.Equal([]string{"private-notes", "team/guide", "team/runbook"}, resultIDs(out.Results))

	_, out, err = s.d.search(as("default"), nil, searchInput{Query: "password", K: 10})
	s.Require().NoError(err)
	s.Empty(out.Results, "second's personal library is not default's to read")
}

func (s *LibrariesSuite) TestLibrariesNarrowsTheSearch() {
	_, out, err := s.d.search(as("default"), nil,
		searchInput{Query: "archive", K: 10, Libraries: []string{"personal"}})
	s.Require().NoError(err)
	s.Equal([]string{"guide"}, resultIDs(out.Results))
}

func (s *LibrariesSuite) TestAQualifiedDocIDScopesTheSearchToThatDocument() {
	_, out, err := s.d.search(as("default"), nil,
		searchInput{Query: "archive", DocID: "team/runbook", K: 5})
	s.Require().NoError(err)
	s.Equal([]string{"team/runbook"}, resultIDs(out.Results))
	s.Equal("team", out.Results[0].Library)
}

func (s *LibrariesSuite) TestADocIDInALibraryTheUserDoesNotReadFindsNothing() {
	// Answered exactly as a document that does not exist.
	for _, docID := range []string{"elsewhere/guide", "lib_team/guide", "second/private-notes"} {
		_, out, err := s.d.search(as("default"), nil, searchInput{Query: "archive", DocID: docID})
		s.Require().NoError(err, docID)
		s.Empty(out.Results, docID)
	}
}

func (s *LibrariesSuite) TestOutlineAndContextFollowTheQualifiedDocID() {
	_, outline, err := s.d.outline(as("second"), nil, outlineInput{DocID: "team/guide"})
	s.Require().NoError(err)
	s.Require().NotEmpty(outline.Entries)
	s.Equal("team/guide", outline.DocID)

	_, hits, err := s.d.search(
		as("second"),
		nil,
		searchInput{Query: "shared drive", DocID: "team/guide"},
	)
	s.Require().NoError(err)
	s.Require().NotEmpty(hits.Results)
	_, got, err := s.d.getContext(as("second"), nil,
		getContextInput{DocID: "team/guide", ChunkID: hits.Results[0].ChunkID})
	s.Require().NoError(err)
	s.Require().NotEmpty(got.Chunks)
	s.Contains(got.Chunks[0].Text, "shared drive")
}
