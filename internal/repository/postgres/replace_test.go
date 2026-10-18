package postgres_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/service/ingest"
	"github.com/bamsammich/docsearch/internal/service/ingest/mocks"
)

// ReplaceSuite re-ingests one source through the real service and the real
// repository, because whether the old version survives is a question about
// what the database holds when the second ingest stops.
type ReplaceSuite struct {
	suite.Suite
	pg      *pgtest.DB
	service *ingest.Service
}

func TestReplace(t *testing.T) { suite.Run(t, new(ReplaceSuite)) }

func (s *ReplaceSuite) SetupTest() {
	s.pg = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.pg.Owner))
	s.service = ingest.New(postgres.New(s.pg.App, builtinID), time.Now)
}

// version is the guide at one revision, saying body in each section.
func (s *ReplaceSuite) version(digest, body string) *mocks.MockSource {
	source := mocks.NewMockSource(s.T())
	source.EXPECT().Kind().Return(domain.SourceKindFile).Maybe()
	source.EXPECT().Identity().Return("/library/guide.md").Maybe()
	source.EXPECT().Digest().Return(digest).Maybe()
	source.EXPECT().Acquire(mock.Anything, mock.Anything).Return(nil)
	source.EXPECT().Extract(mock.Anything, mock.Anything).Return(domain.NewExtraction(
		"Guide", "markdown", domain.SourceATXHeadings, []domain.Block{
			domain.NewOffsetBlock([]string{"Install"}, 0, body+" Unpack the archive and run it."),
			domain.NewOffsetBlock([]string{"Usage"}, 60, body+" Point the tool at a file."),
		}), nil)
	return source
}

func (s *ReplaceSuite) one(query string) string {
	var out string
	pgtest.ReadAs(s.T(), s.pg.App, builtinID, func(tx *sql.Tx) {
		s.Require().NoError(tx.QueryRowContext(s.T().Context(), query).Scan(&out))
	})
	return out
}

func (s *ReplaceSuite) ingestFirst() {
	_, err := s.service.Run(s.T().Context(), s.version("v1", "First"), ingest.Options{})
	s.Require().NoError(err)
}

// A re-ingest stopped part way, by a cancel, a deploy or a dropped
// connection, leaves the previous version searchable and untouched.
func (s *ReplaceSuite) TestAStoppedReplacementKeepsTheOldVersion() {
	s.ingestFirst()
	ctx, cancel := context.WithCancel(s.T().Context())
	defer cancel()
	_, err := s.service.Run(ctx, s.version("v2", "Second"), ingest.Options{
		Progress: func(phase ingest.Phase, _, _ int) {
			if phase == ingest.PhaseIndex {
				cancel()
			}
		},
	})
	s.Require().Error(err)

	s.Equal("1", s.one(`SELECT count(*) FROM documents`))
	s.Equal("ready", s.one(`SELECT status FROM documents`))
	s.Equal("true", s.one(`SELECT (count(*) > 0)::text FROM chunks WHERE text LIKE 'First%'`))
	s.Equal("0", s.one(`SELECT count(*) FROM chunks WHERE text LIKE 'Second%'`))
}

// A finished replacement takes the old identifier, and nothing of the old
// version or the staging copy is left.
func (s *ReplaceSuite) TestAFinishedReplacementTakesTheOldPlace() {
	s.ingestFirst()
	docID := s.one(`SELECT doc_id FROM documents`)
	result, err := s.service.Run(s.T().Context(), s.version("v2", "Second"), ingest.Options{})
	s.Require().NoError(err)

	s.Equal(docID, result.DocID)
	s.Equal(docID, s.one(`SELECT doc_id FROM documents`))
	s.Equal("ready", s.one(`SELECT status FROM documents`))
	s.Equal("0", s.one(`SELECT count(*) FROM chunks WHERE text LIKE 'First%'`))
	s.Equal("true", s.one(`SELECT (count(*) > 0)::text FROM chunks WHERE doc_id = '`+docID+`'`))
	s.Equal("0", s.one(`SELECT count(*) FROM chunks WHERE doc_id LIKE '%~next'`))
}

// A replacement that died before publishing left staging rows; the next
// one clears them and publishes cleanly.
func (s *ReplaceSuite) TestADeadReplacementsRowsAreCleared() {
	s.ingestFirst()
	docID := s.one(`SELECT doc_id FROM documents`)
	repo := postgres.New(s.pg.App, builtinID)
	s.Require().NoError(repo.Create(s.T().Context(), ingest.Document{
		DocID: ingest.Staging(docID), Title: "Guide", Format: "markdown",
		Identity: "/library/guide.md", Digest: "dead", Kind: domain.SourceKindFile,
	}, ""))

	_, err := s.service.Run(s.T().Context(), s.version("v2", "Second"), ingest.Options{})
	s.Require().NoError(err)
	s.Equal("1", s.one(`SELECT count(*) FROM documents`))
}
