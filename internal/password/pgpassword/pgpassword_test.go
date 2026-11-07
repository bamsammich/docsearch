package pgpassword_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/password/pgpassword"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
)

// StoreSuite administers accounts as the owner and signs in as the request
// role, the two credentials password mode uses in production.
type StoreSuite struct {
	suite.Suite
	pg    *pgtest.DB
	admin *pgpassword.Store
	app   *pgpassword.Store
}

func TestStore(t *testing.T) { suite.Run(t, new(StoreSuite)) }

func (s *StoreSuite) SetupTest() {
	s.pg = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.pg.Owner))
	s.admin = pgpassword.New(s.pg.Owner)
	s.app = pgpassword.New(s.pg.App)
}

// An account comes with a library of its own, whose identity is the pair a
// docsearch-issued token resolves to.
func (s *StoreSuite) TestAnAccountComesWithItsOwnLibrary() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))

	var issuer, subject string
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT issuer, subject FROM users WHERE user_id = 'user-a'`).Scan(&issuer, &subject))
	s.Equal("docsearch", issuer)
	s.Equal("user-a", subject)

	var partitions int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_class WHERE relname = 'chunks_user-a'`).Scan(&partitions))
	s.Equal(1, partitions, "the library is partitioned like any other")

	record, found, err := s.app.Find(ctx, "alice")
	s.Require().NoError(err)
	s.True(found)
	s.Equal("user-a", record.UserID)
	s.Equal("hash", record.Hash)
}

func (s *StoreSuite) TestTheBuiltinLibraryIsTakenOnce() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", owner.Builtin, "hash", true))
	s.Require().ErrorIs(s.admin.Add(ctx, "bob", owner.Builtin, "hash", true),
		password.ErrBuiltinTaken)

	_, found, err := s.app.Find(ctx, "bob")
	s.Require().NoError(err)
	s.False(found, "the refused account left nothing behind")
}

func (s *StoreSuite) TestFailuresLockAndANewPasswordUnlocks() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	at := time.Now().Truncate(time.Second)
	attempt := password.Attempt{At: at, LockUntil: at.Add(time.Hour), Threshold: 2}
	for range 2 {
		open, err := s.app.Begin(ctx, "alice", attempt)
		s.Require().NoError(err)
		s.True(open)
	}
	record, _, err := s.app.Find(ctx, "alice")
	s.Require().NoError(err)
	s.Equal(2, record.Failures)
	s.True(record.LockedUntil.Equal(attempt.LockUntil), "the second attempt reached the threshold")

	open, err := s.app.Begin(ctx, "alice", attempt)
	s.Require().NoError(err)
	s.False(open, "a locked account refuses the attempt")
	record, _, err = s.app.Find(ctx, "alice")
	s.Require().NoError(err)
	s.Equal(2, record.Failures, "a refused attempt counts for nothing")

	s.Require().NoError(s.admin.SetHash(ctx, "alice", "new-hash"))
	record, _, err = s.app.Find(ctx, "alice")
	s.Require().NoError(err)
	s.Zero(record.Failures)
	s.True(record.LockedUntil.IsZero())
	s.Equal("new-hash", record.Hash)
}

// A lock that has run out starts the count again, so one wrong guess after
// it ends does not lock the account straight back.
func (s *StoreSuite) TestAnExpiredLockStartsTheCountAgain() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	at := time.Now().Truncate(time.Second)
	attempt := password.Attempt{At: at, LockUntil: at.Add(time.Minute), Threshold: 2}
	for range 2 {
		_, err := s.app.Begin(ctx, "alice", attempt)
		s.Require().NoError(err)
	}

	later := password.Attempt{
		At: at.Add(2 * time.Minute), LockUntil: at.Add(3 * time.Minute), Threshold: 2,
	}
	open, err := s.app.Begin(ctx, "alice", later)
	s.Require().NoError(err)
	s.True(open)
	record, _, err := s.app.Find(ctx, "alice")
	s.Require().NoError(err)
	s.Equal(1, record.Failures)
	s.True(record.LockedUntil.IsZero(), "one attempt is under the threshold")
}

// Guesses sent at once are each counted before any is checked, so no more
// than the threshold of them get to compare a password.
func (s *StoreSuite) TestGuessesSentAtOnceCannotPassTheThreshold() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	at := time.Now().Truncate(time.Second)
	attempt := password.Attempt{At: at, LockUntil: at.Add(time.Hour), Threshold: 3}
	var opened atomic.Int32
	var guesses sync.WaitGroup
	failures := make(chan error, 20)
	for range 20 {
		guesses.Go(func() {
			open, err := s.app.Begin(ctx, "alice", attempt)
			failures <- err
			if open {
				opened.Add(1)
			}
		})
	}
	guesses.Wait()
	close(failures)
	for err := range failures {
		s.Require().NoError(err)
	}
	s.Equal(int32(3), opened.Load())
}

// Removing an account ends its sign-in, and the library stays.
func (s *StoreSuite) TestRemovingAnAccountEndsItsSignIn() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	present, err := s.app.Exists(ctx, "user-a")
	s.Require().NoError(err)
	s.True(present)

	s.Require().NoError(s.admin.Remove(ctx, "alice", password.Handover{}))
	present, err = s.app.Exists(ctx, "user-a")
	s.Require().NoError(err)
	s.False(present)

	var libraries int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE user_id = 'user-a'`).Scan(&libraries))
	s.Equal(1, libraries)

	s.Require().ErrorIs(
		s.admin.Remove(ctx, "alice", password.Handover{}),
		password.ErrNoSuchAccount,
	)
	s.Require().ErrorIs(s.admin.SetHash(ctx, "alice", "x"), password.ErrNoSuchAccount)
}

func (s *StoreSuite) TestListShowsEveryAccount() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "bob", "user-b", "hash", false))
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	list, err := s.admin.List(ctx)
	s.Require().NoError(err)
	s.Require().Len(list, 2)
	s.Equal("alice", list[0].Username)
}

// as runs statements as user, the way a request names its user, inside one
// transaction so the setting goes no further.
func (s *StoreSuite) as(user string, statements ...string) {
	ctx := s.T().Context()
	tx, err := s.pg.Owner.BeginTx(ctx, nil)
	s.Require().NoError(err)
	_, err = tx.ExecContext(ctx, `SELECT set_config('app.user_id', $1, true)`, user)
	s.Require().NoError(err)
	for _, statement := range statements {
		_, err = tx.ExecContext(ctx, statement)
		s.Require().NoError(err)
	}
	s.Require().NoError(tx.Commit())
}

// Removing an account frees its name, so the name can be given again.
func (s *StoreSuite) TestARemovedUsernameCanBeAddedAgain() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	s.Require().NoError(s.admin.Remove(ctx, "alice", password.Handover{}))

	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a2", "hash", false))
	var owner string
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT user_id FROM users WHERE username = 'alice'`).Scan(&owner))
	s.Equal("user-a2", owner, "the name goes to the new account, not back to the old one")
}

// admins makes each username a docsearch administrator, in the order given.
func (s *StoreSuite) admins(usernames ...string) {
	for _, username := range usernames {
		_, err := s.pg.Owner.ExecContext(s.T().Context(),
			`UPDATE users SET admin = true, created_at = clock_timestamp() WHERE username = $1`,
			username)
		s.Require().NoError(err)
	}
}

// adminsOf are the usernames that administer the shared library slug.
func (s *StoreSuite) adminsOf(slug string) []string {
	rows, err := s.pg.Owner.QueryContext(s.T().Context(), `
		SELECT u.username FROM library_grants g
		  JOIN libraries l ON l.library_id = g.library_id
		  JOIN users u ON u.user_id = g.user_id
		 WHERE l.slug = $1 AND g.role = 'admin' ORDER BY u.username`, slug)
	s.Require().NoError(err)
	defer func() { s.Require().NoError(rows.Close()) }()
	var out []string
	for rows.Next() {
		var name string
		s.Require().NoError(rows.Scan(&name))
		out = append(out, name)
	}
	s.Require().NoError(rows.Err())
	return out
}

// A shared library only the removed account administered passes to the
// administrator named as heir, and the grant is recorded.
func (s *StoreSuite) TestASharedLibraryPassesToTheNamedAdministrator() {
	ctx := s.T().Context()
	for name, id := range map[string]string{"alice": "user-a", "bob": "user-b", "carol": "user-c"} {
		s.Require().NoError(s.admin.Add(ctx, name, id, "hash", false))
	}
	s.admins("carol", "bob")
	s.as("user-a", `SELECT create_library('team', 'Team docs')`)

	s.Require().NoError(s.app.Remove(owner.With(ctx, "user-b"), "alice",
		password.Handover{To: "bob"}))

	s.Equal([]string{"bob"}, s.adminsOf("team"))
	var granted int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx, `
		SELECT count(*) FROM access_events
		 WHERE action = 'grant' AND actor = 'user-b' AND subject = 'user-b'
		   AND role = 'admin'`).Scan(&granted))
	s.Equal(1, granted, "the log names bob as the one who removed alice")
}

// With no heir named, or one who is no administrator, the longest-standing
// other administrator takes the library over.
func (s *StoreSuite) TestWithoutAnAdministratorNamedTheLongestStandingTakesOver() {
	ctx := s.T().Context()
	for name, id := range map[string]string{
		"alice": "user-a", "bob": "user-b", "carol": "user-c", "dave": "user-d",
	} {
		s.Require().NoError(s.admin.Add(ctx, name, id, "hash", false))
	}
	s.admins("carol", "bob")
	s.as("user-a", `SELECT create_library('team', 'Team docs')`,
		`SELECT create_library('ops', 'Ops docs')`)
	s.as("user-a", `SELECT grant_library('ops', 'dave', 'editor')`)

	s.Require().NoError(s.app.Remove(owner.With(ctx, "user-b"), "alice",
		password.Handover{To: "dave"}))

	s.Equal([]string{"carol"}, s.adminsOf("team"))
	s.Equal([]string{"carol"}, s.adminsOf("ops"))
}

// The heir takes the library over even when invited to it, and the
// invitation is spent.
func (s *StoreSuite) TestAnInvitedHeirBecomesAdmin() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	s.Require().NoError(s.admin.Add(ctx, "bob", "user-b", "hash", false))
	s.admins("bob")
	s.as("user-a", `SELECT create_library('team', 'Team docs')`,
		`SELECT grant_library('team', 'bob', 'reader')`)

	s.Require().NoError(s.app.Remove(owner.With(ctx, "user-b"), "alice",
		password.Handover{To: "bob"}))

	s.Equal([]string{"bob"}, s.adminsOf("team"))
	var open int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT count(*) FROM library_invitations WHERE user_id = 'user-b'`).Scan(&open))
	s.Equal(0, open)
}

// Only when docsearch has no other administrator is the removal refused,
// naming the libraries it would strand.
func (s *StoreSuite) TestWithNoAdministratorToTakeOverTheRemovalIsRefused() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	s.admins("alice")
	s.as("user-a", `SELECT create_library('team', 'Team docs')`)

	err := s.admin.Remove(ctx, "alice", password.Handover{})
	s.Require().ErrorIs(err, password.ErrStranded)
	s.Require().ErrorContains(err, "Team docs")

	present, err := s.app.Exists(ctx, "user-a")
	s.Require().NoError(err)
	s.True(present, "a refused removal leaves the sign-in in place")
}

// A removed account stops being an administrator.
func (s *StoreSuite) TestARemovedAccountIsNoLongerAnAdministrator() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	s.admins("alice")

	s.Require().NoError(s.admin.Remove(ctx, "alice", password.Handover{}))

	var admin bool
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT admin FROM users WHERE user_id = 'user-a'`).Scan(&admin))
	s.False(admin)
}

// Setting someone's password ends every sign-in they hold, and a missing
// account is refused.
func (s *StoreSuite) TestAResetEndsTheAccountsSignIns() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	for _, statement := range []string{
		`INSERT INTO oauth_clients (client_id, client_name, redirect_uris)
		   VALUES ('client', 'test', '[]')`,
		`INSERT INTO oauth_refresh_tokens
		   (token_hash, client_id, user_id, scopes, audience, auth_time, expires_at)
		   VALUES ('t', 'client', 'user-a', '[]', '[]', now(), now() + interval '1 day')`,
	} {
		_, err := s.pg.Owner.ExecContext(ctx, statement)
		s.Require().NoError(err)
	}

	s.Require().NoError(s.admin.Reset(ctx, "alice", "new-hash"))

	var tokens int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT count(*) FROM oauth_refresh_tokens WHERE user_id = 'user-a'`).Scan(&tokens))
	s.Equal(0, tokens)
	s.Require().ErrorIs(s.admin.Reset(ctx, "nobody", "x"), password.ErrNoSuchAccount)
}

// Once someone else administers the library, removing the account takes it
// out of the shared library and keeps its own.
func (s *StoreSuite) TestARemovedAccountLeavesSharedLibrariesAndKeepsItsOwn() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	s.Require().NoError(s.admin.Add(ctx, "bob", "user-b", "hash", false))
	s.as("user-a", `SELECT create_library('team', 'Team docs')`,
		`SELECT grant_library('team', 'bob', 'admin')`)
	s.as("user-b", `SELECT accept_invitation('team')`)

	s.Require().NoError(s.admin.Remove(ctx, "alice", password.Handover{}))

	var shared, own, revoked int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE l.kind = 'shared'), count(*) FILTER (WHERE l.kind = 'personal')
		  FROM library_grants g JOIN libraries l ON l.library_id = g.library_id
		 WHERE g.user_id = 'user-a'`).Scan(&shared, &own))
	s.Require().NoError(s.pg.Owner.QueryRowContext(
		ctx,
		`SELECT count(*) FROM access_events WHERE action = 'revoke' AND subject = 'user-a'`,
	).Scan(&revoked))
	s.Equal(0, shared)
	s.Equal(1, own)
	s.Equal(1, revoked)
}

// Removing an account withdraws its open invitations, so none waits for an
// account nobody can sign in as.
func (s *StoreSuite) TestARemovedAccountsInvitationsAreWithdrawn() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	s.Require().NoError(s.admin.Add(ctx, "bob", "user-b", "hash", false))
	s.as("user-b", `SELECT create_library('team', 'Team docs')`,
		`SELECT grant_library('team', 'alice', 'reader')`)

	s.Require().NoError(s.admin.Remove(ctx, "alice", password.Handover{}))

	var open, withdrawn int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT count(*) FROM library_invitations WHERE user_id = 'user-a'`).Scan(&open))
	s.Require().NoError(s.pg.Owner.QueryRowContext(
		ctx,
		`SELECT count(*) FROM access_events WHERE action = 'withdraw' AND subject = 'user-a'`,
	).Scan(&withdrawn))
	s.Equal(0, open)
	s.Equal(1, withdrawn)
}

// sqlstate is the SQLSTATE err carries, or empty when it carries none.
func sqlstate(err error) string {
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// insufficientPrivilege is the SQLSTATE of a statement the role holds no
// grant for.
const insufficientPrivilege = "42501"

// notAdministrator is the SQLSTATE an account definer refuses with.
const notAdministrator = "DSA01"

// The request role reads accounts and updates what signing in writes, and
// nothing more: adding a row, removing one, or renaming one is refused.
func (s *StoreSuite) TestTheRequestRoleCannotWriteAccountsDirectly() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	s.Require().NoError(s.admin.Add(ctx, "bob", "user-b", "hash", false))

	for _, statement := range []string{
		`INSERT INTO password_accounts (username, user_id, password_hash)
		   VALUES ('mallory', 'user-b', 'hash')`,
		`DELETE FROM password_accounts WHERE username = 'alice'`,
		`UPDATE password_accounts SET username = 'mallory' WHERE username = 'alice'`,
		`UPDATE password_accounts SET user_id = 'user-b' WHERE username = 'alice'`,
	} {
		_, err := s.pg.App.ExecContext(ctx, statement)
		s.Equal(insufficientPrivilege, sqlstate(err), statement)
	}

	s.Require().NoError(s.app.SetHash(ctx, "alice", "rehashed"),
		"a correct password is rehashed as the request role")
	s.Require().NoError(s.app.Succeeded(ctx, "alice"))
	record, found, err := s.app.Find(ctx, "alice")
	s.Require().NoError(err)
	s.True(found)
	s.Equal("rehashed", record.Hash)
}

// Each account change asked for by someone who is no administrator, or by
// nobody at all, is refused by the database and changes nothing.
func (s *StoreSuite) TestANonAdministratorCannotManageAccounts() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	s.Require().NoError(s.admin.Add(ctx, "bob", "user-b", "hash", false))

	for name, asker := range map[string]context.Context{
		"a member": owner.With(ctx, "user-a"),
		"nobody":   ctx,
	} {
		err := s.app.Add(asker, "mallory", "user-m", "hash", false)
		s.Equal(notAdministrator, sqlstate(err), "add, asked by %s", name)
		err = s.app.Reset(asker, "bob", "new-hash")
		s.Equal(notAdministrator, sqlstate(err), "reset, asked by %s", name)
		err = s.app.Remove(asker, "bob", password.Handover{})
		s.Equal(notAdministrator, sqlstate(err), "remove, asked by %s", name)
	}

	_, found, err := s.app.Find(ctx, "mallory")
	s.Require().NoError(err)
	s.False(found, "the refused add left no sign-in")
	var users int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE user_id = 'user-m'`).Scan(&users))
	s.Zero(users, "the refused add left no library")
	record, found, err := s.app.Find(ctx, "bob")
	s.Require().NoError(err)
	s.True(found, "the refused removal left the sign-in")
	s.Equal("hash", record.Hash, "the refused reset left the password")
}

// An administrator manages accounts through the request role, as the
// library UI does.
func (s *StoreSuite) TestAnAdministratorManagesAccountsAsTheRequestRole() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	s.admins("alice")
	asAlice := owner.With(ctx, "user-a")

	s.Require().NoError(s.app.Add(asAlice, "bob", "user-b", "hash", false))
	s.Require().ErrorIs(s.app.Add(asAlice, "bob", "user-b2", "hash", false), password.ErrTaken)
	s.Require().NoError(s.app.Reset(asAlice, "bob", "new-hash"))
	record, _, err := s.app.Find(ctx, "bob")
	s.Require().NoError(err)
	s.Equal("new-hash", record.Hash)
	s.Require().ErrorIs(s.app.Reset(asAlice, "nobody", "x"), password.ErrNoSuchAccount)

	s.Require().NoError(s.app.Remove(asAlice, "bob", password.Handover{}))
	_, found, err := s.app.Find(ctx, "bob")
	s.Require().NoError(err)
	s.False(found)
	s.Require().ErrorIs(s.app.Remove(asAlice, "bob", password.Handover{}),
		password.ErrNoSuchAccount)
}

// Removing an account takes it out of its groups. A group it alone
// maintained is left to the administrators, not handed to the heir, who
// would otherwise reach the group's libraries.
func (s *StoreSuite) TestARemovedAccountsGroupsAreLeftToTheAdministrators() {
	ctx := s.T().Context()
	s.Require().NoError(s.admin.Add(ctx, "alice", "user-a", "hash", false))
	s.Require().NoError(s.admin.Add(ctx, "bob", "user-b", "hash", false))
	s.admins("bob")
	s.as("user-a", `SELECT create_group('eng', 'Engineering')`)

	s.Require().NoError(s.app.Remove(owner.With(ctx, "user-b"), "alice",
		password.Handover{To: "bob"}))

	var heirs int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx, `
		SELECT count(*) FROM group_members m JOIN groups g ON g.group_id = m.group_id
		 WHERE g.slug = 'eng' AND m.user_id = 'user-b'`).Scan(&heirs))
	s.Zero(heirs, "the heir is not made a member")
	var left int
	s.Require().NoError(s.pg.Owner.QueryRowContext(ctx,
		`SELECT count(*) FROM group_members WHERE user_id = 'user-a'`).Scan(&left))
	s.Zero(left)
}
