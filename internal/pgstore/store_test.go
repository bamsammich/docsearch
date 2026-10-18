package pgstore

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

// ReadySuite covers the readiness probe. Ready is reachable without a token,
// so it runs before any user is known; neither table it reads carries a
// policy, which is what makes that sound.
type ReadySuite struct {
	suite.Suite
	pg *pgtest.DB
}

func TestReady(t *testing.T) { suite.Run(t, new(ReadySuite)) }

func (s *ReadySuite) SetupTest() {
	s.pg = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.pg.Owner))
}

func (s *ReadySuite) TestReadyPassesOnAMigratedDatabase() {
	s.NoError(New(s.pg.App, builtinUser).Ready(s.T().Context()))
}

func (s *ReadySuite) TestReadyNamesTheTableThatIsMissing() {
	_, err := s.pg.Owner.ExecContext(s.T().Context(), `DROP TABLE pages`)
	s.Require().NoError(err)
	err = New(s.pg.App, builtinUser).Ready(s.T().Context())
	s.Require().Error(err, "a database missing a table reads as ready")
	s.Equal("schema incomplete: pages is missing", err.Error())
}
