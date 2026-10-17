package pgupload_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/pgtest"
	"github.com/bamsammich/docsearch/internal/schema"
	"github.com/bamsammich/docsearch/internal/upload"
	"github.com/bamsammich/docsearch/internal/upload/pgupload"
)

// PgUploadSuite runs as the request role against a real database, because
// what it claims is what row-level security and the definer functions do.
type PgUploadSuite struct {
	suite.Suite
	pg *pgtest.DB
}

func TestPgUpload(t *testing.T) { suite.Run(t, new(PgUploadSuite)) }

func (s *PgUploadSuite) SetupTest() {
	s.pg = pgtest.Start(s.T())
	s.Require().NoError(schema.Create(s.T().Context(), s.pg.Owner))
	_, err := s.pg.Owner.ExecContext(s.T().Context(),
		`SELECT create_account('second', 'https://issuer.example', 'subject-2')`)
	s.Require().NoError(err)
}

func file(key, body string) upload.File {
	return upload.File{Key: key, Filename: "manual.pdf", SHA256: upload.Digest([]byte(body)),
		Content: []byte(body)}
}

func (s *PgUploadSuite) TestAnUploadReadsBackAndIsReplacedByKey() {
	store := pgupload.New(s.pg.App, owner.Builtin)
	ctx := s.T().Context()
	s.Require().NoError(store.Put(ctx, file("k", "first")))
	s.Require().NoError(store.Put(ctx, file("k", "second")))

	got, err := store.Get(ctx, "k")
	s.Require().NoError(err)
	s.Equal(file("k", "second"), got)

	_, err = store.Get(ctx, "absent")
	s.Require().ErrorIs(err, upload.ErrNotFound)
}

// One user's uploads are invisible to another, even under the same key.
func (s *PgUploadSuite) TestOneUsersUploadsAreInvisibleToAnother() {
	ctx := s.T().Context()
	s.Require().NoError(pgupload.New(s.pg.App, owner.Builtin).Put(ctx, file("k", "mine")))

	_, err := pgupload.New(s.pg.App, "second").Get(ctx, "k")
	s.Require().ErrorIs(err, upload.ErrNotFound)
}

func (s *PgUploadSuite) TestALinkRedeemsOnceAndNamesItsUser() {
	links := pgupload.NewLinks(s.pg.App)
	ctx := s.T().Context()
	s.Require().NoError(links.Create(ctx, "second", "hash-1", time.Now().Add(time.Minute)))

	open, err := links.Open(ctx, "hash-1")
	s.Require().NoError(err)
	s.True(open, "checking does not spend it")

	user, ok, err := links.Redeem(ctx, "hash-1")
	s.Require().NoError(err)
	s.True(ok)
	s.Equal("second", user)

	_, ok, err = links.Redeem(ctx, "hash-1")
	s.Require().NoError(err)
	s.False(ok, "a link works once")
	open, err = links.Open(ctx, "hash-1")
	s.Require().NoError(err)
	s.False(open)
}

func (s *PgUploadSuite) TestAnExpiredLinkDoesNotRedeem() {
	links := pgupload.NewLinks(s.pg.App)
	ctx := s.T().Context()
	s.Require().NoError(links.Create(ctx, owner.Builtin, "hash-1", time.Now().Add(-time.Minute)))
	_, ok, err := links.Redeem(ctx, "hash-1")
	s.Require().NoError(err)
	s.False(ok)
}
