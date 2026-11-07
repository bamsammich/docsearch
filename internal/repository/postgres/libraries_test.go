package postgres_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

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
	s.join("team", "ben", sharing.Editor)
}

// join has ana invite who to slug with role, and who accept.
func (s *LibrariesSuite) join(slug, who, role string) {
	invited, err := s.as("ana").Grant(s.T().Context(), slug, who, role)
	s.Require().NoError(err)
	s.Require().True(invited)
	_, err = s.as(who).Accept(s.T().Context(), slug)
	s.Require().NoError(err)
}

// grant is by's grant of role on slug to who, for its refusal alone.
func (s *LibrariesSuite) grant(by, slug, who, role string) error {
	_, err := s.as(by).Grant(s.T().Context(), slug, who, role)
	return err
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
	asEditor := s.grant("ben", "team", "cy", sharing.Reader)
	asStranger := s.grant("cy", "team", "cy", sharing.Admin)
	nowhere := s.grant("ana", "nowhere", "cy", sharing.Reader)
	s.Require().ErrorIs(asEditor, sharing.ErrNoLibrary)
	s.Require().ErrorIs(asStranger, sharing.ErrNoLibrary)
	s.Require().ErrorIs(nowhere, sharing.ErrNoLibrary)
}

func (s *LibrariesSuite) TestAGrantNeedsAKnownAccountAndARealRole() {
	s.team()
	s.Require().ErrorIs(s.grant("ana", "team", "nobody", sharing.Reader), sharing.ErrNoAccount)
	s.Require().ErrorIs(s.grant("ana", "team", "cy", "owner"), sharing.ErrInvalid)
}

func (s *LibrariesSuite) TestALibraryAlwaysKeepsAnAdmin() {
	s.team()
	ctx := s.T().Context()
	s.Require().ErrorIs(s.grant("ana", "team", "ana", sharing.Editor), sharing.ErrLastAdmin)
	s.Require().ErrorIs(s.as("ana").Revoke(ctx, "team", "ana"), sharing.ErrLastAdmin)

	s.Require().NoError(s.grant("ana", "team", "ben", sharing.Admin))
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

	s.join("team", "cy", sharing.Reader)
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
	s.Require().Len(events, 4)
	for i, want := range []sharing.Event{
		{Actor: "ana", Action: "create", Subject: "ana", Role: sharing.Admin},
		{Actor: "ana", Action: "invite", Subject: "ben", Role: sharing.Editor},
		{Actor: "ben", Action: "accept", Subject: "ben", Role: sharing.Editor},
		{Actor: "ana", Action: "revoke", Subject: "ben"},
	} {
		want.At = events[i].At
		s.Equal(want, events[i])
	}
}

// An invitation gives nothing until it is accepted: the library is not
// listed, its members are hidden, and nothing can be written into it.
func (s *LibrariesSuite) TestAnInvitationGivesNothingUntilAccepted() {
	s.team()
	ctx := s.T().Context()
	invited, err := s.as("ana").Grant(ctx, "team", "cy", sharing.Reader)
	s.Require().NoError(err)
	s.True(invited)

	libs, err := s.as("cy").List(ctx)
	s.Require().NoError(err)
	s.Len(libs, 1, "only cy's own library")
	_, err = s.as("cy").Members(ctx, "team")
	s.Require().ErrorIs(err, sharing.ErrNoLibrary)
	_, _, err = postgres.NewJobs(s.db.App, pgsession.Scope{User: "cy", Library: s.libraryID("team")}).
		Add(ctx, "https://example.com/docs", "", false)
	s.Require().Error(err, "an invitation is no grant")

	invitations, err := s.as("cy").Invitations(ctx)
	s.Require().NoError(err)
	s.Require().Len(invitations, 1)
	invitations[0].At = time.Time{}
	s.Equal(sharing.Invitation{
		Library: "team", Title: "Team docs", Role: sharing.Reader, InvitedBy: "ana",
	}, invitations[0])

	joined, err := s.as("cy").Accept(ctx, "team")
	s.Require().NoError(err)
	s.Equal(sharing.Library{Name: "team", Title: "Team docs", Role: sharing.Reader}, joined)
	libs, err = s.as("cy").List(ctx)
	s.Require().NoError(err)
	s.Len(libs, 2)
	invitations, err = s.as("cy").Invitations(ctx)
	s.Require().NoError(err)
	s.Empty(invitations)
	_, err = s.as("cy").Accept(ctx, "team")
	s.Require().ErrorIs(err, sharing.ErrNoInvitation, "an invitation is accepted once")
}

// An admin sees who is invited, and nobody else does.
func (s *LibrariesSuite) TestOnlyAnAdminSeesWhoIsInvited() {
	s.team()
	ctx := s.T().Context()
	s.Require().NoError(s.grant("ana", "team", "cy", sharing.Reader))

	members, err := s.as("ana").Members(ctx, "team")
	s.Require().NoError(err)
	s.Equal([]sharing.Member{
		{Username: "ana", Role: sharing.Admin},
		{Username: "ben", Role: sharing.Editor},
		{Username: "cy", Role: sharing.Reader, Invited: true},
	}, members)

	members, err = s.as("ben").Members(ctx, "team")
	s.Require().NoError(err)
	s.Len(members, 2, "an editor does not see invitations")
}

// Inviting someone again changes the role offered, and granting a member
// changes their role without inviting them.
func (s *LibrariesSuite) TestInvitingAgainChangesTheRoleOffered() {
	s.team()
	ctx := s.T().Context()
	s.Require().NoError(s.grant("ana", "team", "cy", sharing.Reader))
	s.Require().NoError(s.grant("ana", "team", "cy", sharing.Admin))
	joined, err := s.as("cy").Accept(ctx, "team")
	s.Require().NoError(err)
	s.Equal(sharing.Admin, joined.Role)

	invited, err := s.as("ana").Grant(ctx, "team", "ben", sharing.Reader)
	s.Require().NoError(err)
	s.False(invited, "ben is a member, so the grant changes his role")
}

// An invitee declines, an admin withdraws, and either leaves nothing to
// accept. A library with no invitation answers alike, real or not.
func (s *LibrariesSuite) TestAnInvitationIsDeclinedOrWithdrawn() {
	s.team()
	ctx := s.T().Context()
	s.Require().NoError(s.grant("ana", "team", "cy", sharing.Reader))
	s.Require().NoError(s.as("cy").Decline(ctx, "team"))
	_, err := s.as("cy").Accept(ctx, "team")
	s.Require().ErrorIs(err, sharing.ErrNoInvitation)

	s.Require().NoError(s.grant("ana", "team", "cy", sharing.Reader))
	s.Require().NoError(s.as("ana").Revoke(ctx, "team", "cy"))
	_, err = s.as("cy").Accept(ctx, "team")
	s.Require().ErrorIs(err, sharing.ErrNoInvitation)

	s.Require().ErrorIs(s.as("cy").Decline(ctx, "nowhere"), sharing.ErrNoInvitation)
	s.Require().ErrorIs(s.as("ben").Decline(ctx, "team"), sharing.ErrNoInvitation)

	events, err := s.as("ana").Events(ctx, "team")
	s.Require().NoError(err)
	actions := make([]string, 0, len(events))
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	s.Equal([]string{
		"create", "invite", "accept", "invite", "decline", "invite", "withdraw",
	}, actions)
}

// A library's name has a limit, and so does how many one account creates.
func (s *LibrariesSuite) TestLibrariesHaveLimits() {
	ctx := s.T().Context()
	_, err := s.as("cy").Create(ctx, "long", strings.Repeat("n", 121))
	s.Require().ErrorIs(err, sharing.ErrInvalid)

	for i := range 100 {
		_, err := s.as("cy").Create(ctx, fmt.Sprintf("lib-%d", i), "Library")
		s.Require().NoError(err)
	}
	_, err = s.as("cy").Create(ctx, "one-too-many", "Library")
	s.Require().ErrorIs(err, sharing.ErrTooMany)
	_, err = s.as("ana").Create(ctx, "another", "Library")
	s.Require().NoError(err, "the limit is per account")
}

// The change log keeps a username once the account has lost it, as a
// removed account does.
func (s *LibrariesSuite) TestTheLogKeepsAUsernameTheAccountLost() {
	s.team()
	ctx := s.T().Context()
	_, err := s.db.Owner.ExecContext(ctx, `UPDATE users SET username = NULL WHERE user_id = 'ben'`)
	s.Require().NoError(err)

	events, err := s.as("ana").Events(ctx, "team")
	s.Require().NoError(err)
	s.Require().Len(events, 3)
	s.Equal("ben", events[1].Subject)
	s.Equal("ben", events[2].Actor)
}

// An admin renames a library; its slug stays, and the log keeps the old
// name. Nobody else may.
func (s *LibrariesSuite) TestAnAdminRenamesALibrary() {
	s.team()
	ctx := s.T().Context()
	_, err := s.as("ben").Rename(ctx, "team", "Ben's docs")
	s.Require().ErrorIs(err, sharing.ErrNoLibrary, "an editor is no admin")
	_, err = s.as("ana").Rename(ctx, "team", "  ")
	s.Require().ErrorIs(err, sharing.ErrInvalid)

	renamed, err := s.as("ana").Rename(ctx, "team", "Platform docs")
	s.Require().NoError(err)
	s.Equal(sharing.Library{Name: "team", Title: "Platform docs", Role: sharing.Admin}, renamed)
	events, err := s.as("ana").Events(ctx, "team")
	s.Require().NoError(err)
	last := events[len(events)-1]
	s.Equal("rename", last.Action)
	s.Equal("Team docs", last.Detail)
}

// Deleting a library takes it from every member at once and fails its
// jobs; an admin restores it within the grace period.
func (s *LibrariesSuite) TestADeletedLibraryLeavesEveryoneUntilRestored() {
	s.team()
	ctx := s.T().Context()
	jobs := postgres.NewJobs(s.db.App, pgsession.Scope{User: "ben", Library: s.libraryID("team")})
	id, _, err := jobs.Add(ctx, "https://example.com/docs", "", false)
	s.Require().NoError(err)

	_, err = s.as("ben").Delete(ctx, "team")
	s.Require().ErrorIs(err, sharing.ErrNoLibrary, "an editor is no admin")
	purgeAt, err := s.as("ana").Delete(ctx, "team")
	s.Require().NoError(err)
	s.WithinDuration(time.Now().Add(14*24*time.Hour), purgeAt, time.Minute)

	for _, who := range []string{"ana", "ben"} {
		libs, err := s.as(who).List(ctx)
		s.Require().NoError(err)
		s.Len(libs, 1, who+" reads only their own library")
	}
	_, _, err = jobs.Add(ctx, "https://example.com/more", "", false)
	s.Require().Error(err, "nothing is written into a deleted library")
	var status string
	s.Require().NoError(s.db.Owner.QueryRowContext(ctx,
		`SELECT status FROM ingest_jobs WHERE id = $1`, id).Scan(&status))
	s.Equal("failed", status)

	deleted, err := s.as("ben").Deleted(ctx)
	s.Require().NoError(err)
	s.Empty(deleted, "only an admin sees it to restore")
	deleted, err = s.as("ana").Deleted(ctx)
	s.Require().NoError(err)
	s.Require().Len(deleted, 1)
	s.Equal("team", deleted[0].Library)
	s.Equal("ana", deleted[0].DeletedBy)

	_, err = s.as("ben").Restore(ctx, "team")
	s.Require().ErrorIs(err, sharing.ErrNoLibrary)
	restored, err := s.as("ana").Restore(ctx, "team")
	s.Require().NoError(err)
	s.Equal("team", restored.Name)
	libs, err := s.as("ben").List(ctx)
	s.Require().NoError(err)
	s.Len(libs, 2, "ben reads it again")
}

// A personal library has no slug, so it is never deleted.
func (s *LibrariesSuite) TestAPersonalLibraryIsNotDeleted() {
	_, err := s.as("ana").Delete(s.T().Context(), "personal")
	s.Require().ErrorIs(err, sharing.ErrNoLibrary)
}

// Past its grace period a deleted library is purged with everything in it,
// and its slug is free again.
func (s *LibrariesSuite) TestALibraryPastItsGracePeriodIsPurged() {
	s.team()
	ctx := s.T().Context()
	lib := s.libraryID("team")
	_, _, err := postgres.NewJobs(s.db.App, pgsession.Scope{User: "ben", Library: lib}).
		Add(ctx, "https://example.com/docs", "", false)
	s.Require().NoError(err)
	s.document(pgsession.Scope{User: "ben", Library: lib})
	_, err = s.as("ana").Delete(ctx, "team")
	s.Require().NoError(err)

	queues := postgres.NewQueues(s.db.App)
	_, err = queues.Waiting(ctx)
	s.Require().NoError(err)
	s.Equal(1, s.count(`SELECT count(*) FROM libraries WHERE slug = 'team'`),
		"within grace it stays")

	_, err = s.db.Owner.ExecContext(ctx,
		`UPDATE libraries SET deleted_at = now() - interval '15 days' WHERE library_id = $1`, lib)
	s.Require().NoError(err)
	_, err = queues.Waiting(ctx)
	s.Require().NoError(err)

	s.Zero(s.count(`SELECT count(*) FROM libraries WHERE slug = 'team'`))
	s.Zero(s.count(`SELECT count(*) FROM documents WHERE library_id = $1`, lib))
	s.Zero(s.count(`SELECT count(*) FROM pg_class WHERE relname = 'chunks_' || $1`, lib))
	s.Zero(s.count(`SELECT count(*) FROM library_grants WHERE library_id = $1`, lib))
	deleted, err := s.as("ana").Deleted(ctx)
	s.Require().NoError(err)
	s.Empty(deleted)
	_, err = s.as("cy").Create(ctx, "team", "A new team")
	s.Require().NoError(err, "the slug is free once purged")
}

// document writes one document and its chunk into scope's library.
func (s *LibrariesSuite) document(scope pgsession.Scope) {
	ctx := s.T().Context()
	tx, err := s.db.App.BeginTx(ctx, nil)
	s.Require().NoError(err)
	defer func() { s.NoError(pgsession.Rollback(tx)) }()
	for _, statement := range []string{
		`SELECT set_config('app.user_id', $1, true), set_config('app.library_id', $2, true)`,
		`INSERT INTO documents (library_id, doc_id, title, format, source_path, sha256, status)
		   VALUES ($2, 'guide', 'Guide', 'markdown', '/library/guide.md', 'abc', 'ready')
		   RETURNING $1::text`,
		`INSERT INTO chunks (library_id, doc_id, ordinal, heading_path, text)
		   VALUES ($2, 'guide', 0, 'Guide', 'a chunk') RETURNING $1::text`,
	} {
		_, err := tx.ExecContext(ctx, statement, scope.User, scope.Library)
		s.Require().NoError(err)
	}
	s.Require().NoError(tx.Commit())
}

// count runs a count query as the owner.
func (s *LibrariesSuite) count(query string, args ...any) int {
	var n int
	s.Require().NoError(s.db.Owner.QueryRowContext(s.T().Context(), query, args...).Scan(&n))
	return n
}

// An account reads its own username and nobody else's, and one that has not
// signed in since usernames were recorded reads none.
func (s *LibrariesSuite) TestAnAccountReadsItsOwnUsername() {
	name, err := s.as("ben").Username(s.T().Context())
	s.Require().NoError(err)
	s.Equal("ben", name)

	_, err = s.db.Owner.ExecContext(s.T().Context(),
		`SELECT create_account('dee', 'https://issuer.example', 'dee')`)
	s.Require().NoError(err)
	name, err = s.as("dee").Username(s.T().Context())
	s.Require().NoError(err)
	s.Empty(name)
}

// tx is a transaction acting as user, for running two callers at once.
func (s *LibrariesSuite) tx(user string) *sql.Tx {
	tx, err := s.db.App.BeginTx(s.T().Context(), nil)
	s.Require().NoError(err)
	_, err = tx.ExecContext(s.T().Context(), `SELECT set_config('app.user_id', $1, true)`, user)
	s.Require().NoError(err)
	return tx
}

// Two admins demoting each other, or both leaving, at the same moment leave
// the library one of them: the second waits for the first and is refused.
func (s *LibrariesSuite) TestTwoAdminsCannotBothStepDownAtOnce() {
	for name, steps := range map[string][2]string{
		"demote each other": {
			`SELECT grant_library('team', 'ben', 'editor')`,
			`SELECT grant_library('team', 'ana', 'editor')`,
		},
		"both leave": {
			`SELECT revoke_library('team', 'ana')`,
			`SELECT revoke_library('team', 'ben')`,
		},
	} {
		s.Run(name, func() {
			s.SetupTest()
			s.team()
			s.Require().NoError(s.grant("ana", "team", "ben", sharing.Admin))
			first, second := s.tx("ana"), s.tx("ben")
			_, err := first.ExecContext(s.T().Context(), steps[0])
			s.Require().NoError(err)
			refused := make(chan error, 1)
			go func() {
				_, err := second.ExecContext(s.T().Context(), steps[1])
				refused <- err
			}()
			s.Require().Eventually(func() bool {
				return s.count(`SELECT count(*) FROM pg_locks WHERE NOT granted`) > 0
			}, 10*time.Second, 20*time.Millisecond, "the second waits on the library's lock")
			s.Require().NoError(first.Commit())
			s.Require().ErrorContains(<-refused, "without an admin")
			s.Require().NoError(second.Rollback())
			s.Equal(
				1,
				s.count(`SELECT count(*) FROM library_grants g JOIN libraries l USING (library_id)
				WHERE l.slug = 'team' AND g.role = 'admin'`),
			)
		})
	}
}

// A library name holds no control characters, which a terminal printing
// `docsearch library list` would obey.
func (s *LibrariesSuite) TestALibraryNameHoldsNoControlCharacters() {
	ctx := s.T().Context()
	_, err := s.as("ana").Create(ctx, "ops", "Ops\x1b]8;;https://example.com\x07")
	s.Require().ErrorIs(err, sharing.ErrLibraryName)
	s.team()
	_, err = s.as("ana").Rename(ctx, "team", "Team\rdocs")
	s.Require().ErrorIs(err, sharing.ErrLibraryName)
	_, err = s.as("ana").Rename(ctx, "team", "Team handbook")
	s.Require().NoError(err)
}
