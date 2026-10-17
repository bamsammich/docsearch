package docsync_test

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/docsync"
	"github.com/bamsammich/docsearch/internal/docsync/mocks"
	"github.com/bamsammich/docsearch/internal/paperless"
	"github.com/bamsammich/docsearch/internal/service/document"
	"github.com/bamsammich/docsearch/internal/service/job"
	"github.com/bamsammich/docsearch/internal/upload"
)

var (
	monday  = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	tuesday = monday.Add(24 * time.Hour)
)

func options() docsync.Options {
	return docsync.Options{
		Prefix: "paperless",
		Supports: func(name string) bool {
			ext := filepath.Ext(name)
			return ext == ".pdf" || ext == ".md"
		},
	}
}

func manual(id int, modified time.Time) paperless.Document {
	return paperless.Document{
		ID: id, Title: "Manual", Modified: modified,
		OriginalFileName: "manual.pdf", ArchivedFileName: "manual-archive.pdf",
	}
}

func held(key string, ingested time.Time) document.Document {
	return document.Document{DocID: "doc-" + key, Source: upload.Target(key), IngestedAt: ingested}
}

type PlanSuite struct {
	suite.Suite
}

func TestPlan(t *testing.T) { suite.Run(t, new(PlanSuite)) }

func (s *PlanSuite) TestADocumentTheLibraryLacksIsPushedAsItsOriginal() {
	got := docsync.Plan([]paperless.Document{manual(7, monday)}, nil, nil, options())
	s.Equal([]docsync.Action{{
		Kind: docsync.Push, Key: "paperless-7", ID: 7, Filename: "manual.pdf", Original: true,
	}}, got)
}

// Unchanged is decided without any state on disk: docsearch read it after
// Paperless last changed it.
func (s *PlanSuite) TestADocumentReadSinceItChangedIsSkipped() {
	got := docsync.Plan([]paperless.Document{manual(7, monday)},
		[]document.Document{held("paperless-7", tuesday)}, nil, options())
	s.Require().Len(got, 1)
	s.Equal(docsync.Skip, got[0].Kind)
	s.Equal("unchanged", got[0].Reason)
}

func (s *PlanSuite) TestADocumentChangedSinceItWasReadIsPushedAgain() {
	got := docsync.Plan([]paperless.Document{manual(7, tuesday)},
		[]document.Document{held("paperless-7", monday)}, nil, options())
	s.Require().Len(got, 1)
	s.Equal(docsync.Push, got[0].Kind)
}

// A document still being read is not pushed again on top of itself.
func (s *PlanSuite) TestADocumentBeingReadIsLeftAlone() {
	got := docsync.Plan([]paperless.Document{manual(7, monday)}, nil,
		[]job.Job{{Source: "upload:paperless-7", Status: "running"}}, options())
	s.Require().Len(got, 1)
	s.Equal(docsync.Skip, got[0].Kind)
	s.Equal("already being read", got[0].Reason)
}

func (s *PlanSuite) TestTheArchiveIsUsedWhereTheOriginalCannotBeRead() {
	photo := paperless.Document{ID: 8, OriginalFileName: "page.jpg", ArchivedFileName: "page.pdf"}
	none := paperless.Document{ID: 9, OriginalFileName: "page.jpg"}
	got := docsync.Plan([]paperless.Document{photo, none}, nil, nil, options())
	s.Require().Len(got, 2)
	s.Equal("page.pdf", got[0].Filename)
	s.False(got[0].Original)
	s.Equal(docsync.Skip, got[1].Kind)
	s.Equal("no copy docsearch can read", got[1].Reason)
}

func (s *PlanSuite) TestPreferArchiveTakesTheArchiveWheneverThereIsOne() {
	opts := options()
	opts.PreferArchive = true
	got := docsync.Plan([]paperless.Document{manual(7, monday)}, nil, nil, opts)
	s.Equal("manual-archive.pdf", got[0].Filename)
	s.False(got[0].Original)
}

// Only this sync's documents are retired: not a file, not an upload by
// hand, not another instance's prefix.
func (s *PlanSuite) TestOnlyThisSyncsVanishedDocumentsAreRetired() {
	library := []document.Document{
		held("paperless-7", tuesday),
		held("paperless-8", tuesday),
		held("other-8", tuesday),
		held("handmade", tuesday),
		{DocID: "file", Source: "/library/manual.pdf"},
	}
	got := docsync.Plan([]paperless.Document{manual(7, monday)}, library, nil, options())
	s.Require().Len(got, 2)
	s.Equal(docsync.Action{Kind: docsync.Retire, Key: "paperless-8", DocID: "doc-paperless-8"},
		got[1])
}

// Retiring a document whose job is still queued would delete the upload the
// job is about to read, so it waits for the next sync.
func (s *PlanSuite) TestADocumentBeingReadIsNotRetired() {
	got := docsync.Plan(nil, []document.Document{held("paperless-8", tuesday)},
		[]job.Job{{Source: "upload:paperless-8", Status: "queued"}}, options())
	s.Empty(got)
}

type RunSuite struct {
	suite.Suite
	paperless *mocks.MockPaperless
	library   *mocks.MockLibrary
	uploader  *mocks.MockUploader
}

func TestRun(t *testing.T) { suite.Run(t, new(RunSuite)) }

func (s *RunSuite) SetupTest() {
	s.paperless = mocks.NewMockPaperless(s.T())
	s.library = mocks.NewMockLibrary(s.T())
	s.uploader = mocks.NewMockUploader(s.T())
}

func (s *RunSuite) syncer(opts docsync.Options) *docsync.Syncer {
	return docsync.New(s.paperless, s.library, s.uploader, opts)
}

func (s *RunSuite) sees(shown []paperless.Document, have []document.Document) {
	s.paperless.EXPECT().List(mock.Anything).Return(shown, nil)
	s.library.EXPECT().List(mock.Anything).Return(have, nil)
	s.library.EXPECT().Jobs(mock.Anything, false, 0).Return(nil, nil)
}

func (s *RunSuite) TestRunPushesAndRetires() {
	s.sees([]paperless.Document{manual(7, monday)},
		[]document.Document{held("paperless-8", tuesday)})
	s.paperless.EXPECT().Download(mock.Anything, 7, true).
		Return(io.NopCloser(strings.NewReader("%PDF")), nil)
	s.uploader.EXPECT().Put(mock.Anything, "manual.pdf", "paperless-7", mock.Anything).
		Return(upload.Received{JobID: 1}, nil)
	s.library.EXPECT().Remove(mock.Anything, "doc-paperless-8").Return(nil)

	got, err := s.syncer(options()).Run(s.T().Context())
	s.Require().NoError(err)
	s.Require().Len(got, 2)
	s.Require().NoError(got[0].Err)
	s.Require().NoError(got[1].Err)
}

// One document that will not download does not stop the next.
func (s *RunSuite) TestOneFailureDoesNotStopTheRest() {
	s.sees([]paperless.Document{manual(7, monday), manual(8, monday)}, nil)
	s.paperless.EXPECT().Download(mock.Anything, 7, true).Return(nil, errors.New("404"))
	s.paperless.EXPECT().Download(mock.Anything, 8, true).
		Return(io.NopCloser(strings.NewReader("%PDF")), nil)
	s.uploader.EXPECT().Put(mock.Anything, "manual.pdf", "paperless-8", mock.Anything).
		Return(upload.Received{}, nil)

	got, err := s.syncer(options()).Run(s.T().Context())
	s.Require().NoError(err)
	s.Require().Error(got[0].Err)
	s.Require().NoError(got[1].Err)
}

// An empty Paperless is far more often a revoked share than an emptied
// library, so retiring everything needs saying twice.
func (s *RunSuite) TestAnEmptyPaperlessRetiresNothingUnlessAllowed() {
	s.sees(nil, []document.Document{held("paperless-8", tuesday)})
	_, err := s.syncer(options()).Run(s.T().Context())
	s.Require().ErrorIs(err, docsync.ErrNothingShown)
	s.library.AssertNotCalled(s.T(), "Remove", mock.Anything, mock.Anything)
}
