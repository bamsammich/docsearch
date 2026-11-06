package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/pgsession"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// LibrariesSuite holds the rules for sharing a library to the database that
// enforces them: who may grant, that a library keeps an admin, and that a
// library the caller cannot act on answers as one that does not exist.
type LibrariesSuite struct {
	suite.Suite
	db *pgtest.DB
}

func TestLibraries(t *testing.T) { suite.Run(t, new(LibrariesSuite)) }

func (s *LibrariesSuite) SetupTest() {
	s.db = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.db.Owner))
	for _, account := range []string{"ana", "ben", "cy"} {
		_, err := s.db.Owner.ExecContext(
			s.T().Context(),
			`SELECT create_account($1, 'https://issuer.example', $1), set_username($1, $1)`,
			account,
		)
		s.Require().NoError(err)
	}
}

func (s *LibrariesSuite) as(user string) *postgres.Libraries {
	return postgres.NewLibraries(s.db.App, user)
}

// team is a library ana administers, with ben as an editor.
func (s *LibrariesSuite) team() {
	_, err := s.as("ana").Create(s.T().Context(), "team", "Team docs")
	s.Require().NoError(err)
	s.Require().NoError(s.as("ana").Grant(s.T().Context(), "team", "ben", sharing.Editor))
}

func (s *LibrariesSuite) TestACreatedLibraryIsListedWithItsCreatorAsAdmin() {
	s.team()

	libs, err := s.as("ana").List(s.T().Context())
	s.Require().NoError(err)
	s.Equal([]sharing.Library{
		{Name: "personal", Role: sharing.Admin},
		{Name: "team", Title: "Team docs", Role: sharing.Admin},
	}, libs)

	libs, err = s.as("cy").List(s.T().Context())
	s.Require().NoError(err)
	s.Len(libs, 1, "cy holds no grant on team")
}

func (s *LibrariesSuite) TestAnEditorCanWriteIntoTheNewLibrary() {
	// The partition is made with the library, so a write reaches it.
	s.team()
	libs, err := s.as("ben").List(s.T().Context())
	s.Require().NoError(err)
	s.Require().Len(libs, 2)

	id, _, err := postgres.NewJobs(s.db.App, pgsession.Scope{User: "ben", Library: s.libraryID("team")}).
		Add(s.T().Context(), "https://example.com/docs", "", false)
	s.Require().NoError(err)
	s.NotZero(id)
}

func (s *LibrariesSuite) libraryID(slug string) string {
	var id string
	s.Require().NoError(s.db.Owner.QueryRowContext(s.T().Context(),
		`SELECT library_id FROM libraries WHERE slug = $1`, slug).Scan(&id))
	return id
}

func (s *LibrariesSuite) TestANameIsTakenOnceAndPersonalIsReserved() {
	s.team()
	_, err := s.as("cy").Create(s.T().Context(), "team", "Another")
	s.Require().ErrorIs(err, sharing.ErrNameTaken)

	for _, slug := range []string{"personal", "Has Caps", "under_score"} {
		_, err := s.as("cy").Create(s.T().Context(), slug, "x")
		s.Require().ErrorIs(err, sharing.ErrInvalid, slug)
	}
}

func (s *LibrariesSuite) TestOnlyAnAdminGrantsAndAStrangerIsToldTheSame() {
	s.team()
	ctx := s.T().Context()

	asEditor := s.as("ben").Grant(ctx, "team", "cy", sharing.Reader)
	asStranger := s.as("cy").Grant(ctx, "team", "cy", sharing.Admin)
	nowhere := s.as("ana").Grant(ctx, "nowhere", "cy", sharing.Reader)
	s.Require().ErrorIs(asEditor, sharing.ErrNoLibrary)
	s.Require().ErrorIs(asStranger, sharing.ErrNoLibrary)
	s.Require().ErrorIs(nowhere, sharing.ErrNoLibrary)
}

func (s *LibrariesSuite) TestAGrantNeedsAKnownAccountAndARealRole() {
	s.team()
	ctx := s.T().Context()
	s.Require().
		ErrorIs(s.as("ana").Grant(ctx, "team", "nobody", sharing.Reader), sharing.ErrNoAccount)
	s.Require().ErrorIs(s.as("ana").Grant(ctx, "team", "cy", "owner"), sharing.ErrInvalid)
}

func (s *LibrariesSuite) TestALibraryAlwaysKeepsAnAdmin() {
	s.team()
	ctx := s.T().Context()
	s.Require().ErrorIs(s.as("ana").Grant(ctx, "team", "ana", sharing.Editor), sharing.ErrLastAdmin)
	s.Require().ErrorIs(s.as("ana").Revoke(ctx, "team", "ana"), sharing.ErrLastAdmin)

	s.Require().NoError(s.as("ana").Grant(ctx, "team", "ben", sharing.Admin))
	s.Require().
		NoError(s.as("ana").Revoke(ctx, "team", "ana"), "with a second admin, the first may leave")
}

func (s *LibrariesSuite) TestAMemberLeavesAndAnAdminRemovesThem() {
	s.team()
	ctx := s.T().Context()
	s.Require().NoError(s.as("ben").Revoke(ctx, "team", "ben"))
	_, err := s.as("ben").Members(ctx, "team")
	s.Require().
		ErrorIs(err, sharing.ErrNoLibrary, "ben left, so team answers as if it were not there")

	s.Require().NoError(s.as("ana").Grant(ctx, "team", "cy", sharing.Reader))
	s.Require().ErrorIs(s.as("ben").Revoke(ctx, "team", "cy"), sharing.ErrNoLibrary)
	s.Require().NoError(s.as("ana").Revoke(ctx, "team", "cy"))
}

func (s *LibrariesSuite) TestMembersAreSeenByMembersAndEventsByAdmins() {
	s.team()
	ctx := s.T().Context()

	members, err := s.as("ben").Members(ctx, "team")
	s.Require().NoError(err)
	s.Equal(
		[]sharing.Member{
			{Username: "ana", Role: sharing.Admin},
			{Username: "ben", Role: sharing.Editor},
		},
		members,
	)
	_, err = s.as("cy").Members(ctx, "team")
	s.Require().ErrorIs(err, sharing.ErrNoLibrary)

	_, err = s.as("ben").Events(ctx, "team")
	s.Require().ErrorIs(err, sharing.ErrNoLibrary, "an editor is no admin")
	s.Require().NoError(s.as("ana").Revoke(ctx, "team", "ben"))
	events, err := s.as("ana").Events(ctx, "team")
	s.Require().NoError(err)
	s.Require().Len(events, 3)
	for i, want := range []sharing.Event{
		{Actor: "ana", Action: "create", Subject: "ana", Role: sharing.Admin},
		{Actor: "ana", Action: "grant", Subject: "ben", Role: sharing.Editor},
		{Actor: "ana", Action: "revoke", Subject: "ben"},
	} {
		want.At = events[i].At
		s.Equal(want, events[i])
	}
}
