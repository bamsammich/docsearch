package library_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/library"
	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/source/site"
)

// ResolveSuite checks how a label or a doc_id becomes a scope, against
// Postgres: the built-in account administers a shared library that a second
// account only reads.
type ResolveSuite struct {
	suite.Suite
	libs *library.Libraries
}

func TestResolve(t *testing.T) { suite.Run(t, new(ResolveSuite)) }

func (s *ResolveSuite) SetupTest() {
	pg := pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), pg.Owner))
	for _, statement := range []string{
		`SELECT create_account('second', 'https://issuer.example', 'subject-2')`,
		`INSERT INTO libraries (library_id, kind, slug, name)
		   VALUES ('lib_team', 'shared', 'team', 'Team docs')`,
		`INSERT INTO library_grants (library_id, user_id, role)
		   VALUES ('lib_team', 'default', 'admin'), ('lib_team', 'second', 'reader')`,
	} {
		_, err := pg.Owner.ExecContext(s.T().Context(), statement)
		s.Require().NoError(err)
	}
	s.libs = library.New(pg.App, nil, nil, nil, site.Options{}, time.Now)
}

func (s *ResolveSuite) TestReadableListsYourOwnFirstThenWhatYouAreGranted() {
	libs, err := s.libs.Readable(s.T().Context(), "second")
	s.Require().NoError(err)
	s.Require().Len(libs, 2)

	s.True(libs[0].Own())
	s.Equal("personal", libs[0].Label)
	s.Equal("team", libs[1].Label)
	s.Equal("reader", libs[1].Role)
}

func (s *ResolveSuite) TestAQualifiedIdentifierIsReadInItsLibrary() {
	scope, local, err := s.libs.Locate(s.T().Context(), "second", "team/guide")
	s.Require().NoError(err)

	s.Equal(pgsession.Scope{User: "second", Library: "lib_team"}, scope)
	s.Equal("guide", local)
}

func (s *ResolveSuite) TestABareIdentifierIsReadInYourOwn() {
	scope, local, err := s.libs.Locate(s.T().Context(), "second", "guide")
	s.Require().NoError(err)

	s.Equal(pgsession.Personal("second"), scope)
	s.Equal("guide", local)
}

// A library someone else holds answers as a document that is not there:
// the identifier is looked up in your own library exactly as sent.
func (s *ResolveSuite) TestALibraryYouDoNotReadIsLookedUpInYourOwnAsSent() {
	scope, local, err := s.libs.Locate(s.T().Context(), "second", "finance/ledger")
	s.Require().NoError(err)

	s.Equal(pgsession.Personal("second"), scope)
	s.Equal("finance/ledger", local)
}

func (s *ResolveSuite) TestOnlyAnEditorGetsADestination() {
	scope, err := s.libs.Destination(s.T().Context(), "default", "team")
	s.Require().NoError(err)
	s.Equal(pgsession.Scope{User: "default", Library: "lib_team"}, scope)

	_, err = s.libs.Destination(s.T().Context(), "second", "team")
	s.Require().ErrorIs(err, library.ErrNotWritable)
}
