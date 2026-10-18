package inspect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/libroot"
	"github.com/bamsammich/docsearch/internal/service/inspect"
	"github.com/bamsammich/docsearch/internal/service/inspect/mocks"
)

// InspectSuite checks that inspect reads only what an ingest could.
type InspectSuite struct {
	suite.Suite
	reader  *mocks.MockReader
	formats *mocks.MockFormats
	service *inspect.Service
	root    string
}

func TestInspect(t *testing.T) { suite.Run(t, new(InspectSuite)) }

func (s *InspectSuite) SetupTest() {
	s.reader = mocks.NewMockReader(s.T())
	s.formats = mocks.NewMockFormats(s.T())
	root, err := filepath.EvalSymlinks(s.T().TempDir())
	s.Require().NoError(err)
	s.root = root
	s.service = inspect.New(s.reader, s.formats, nil, []string{root})
}

// A file outside the roots and a file inside them that is not there are
// refused alike, and neither is opened, so inspect cannot be used to learn
// what exists on the server's disk.
func (s *InspectSuite) TestAPathOutsideTheRootsReadsLikeOneThatIsNotThere() {
	outside := filepath.Join(s.T().TempDir(), "secret.pdf")
	s.Require().NoError(os.WriteFile(outside, []byte("%PDF"), 0o600))

	_, outsideErr := s.service.Inspect(s.T().Context(), outside)
	_, missingErr := s.service.Inspect(s.T().Context(), filepath.Join(s.root, "absent.pdf"))

	s.Require().ErrorIs(outsideErr, libroot.ErrOutsideRoot)
	s.Require().ErrorIs(missingErr, libroot.ErrOutsideRoot)
	s.Equal(libroot.ErrOutsideRoot.Error(), unwrapTarget(outsideErr, outside))
	s.Equal(libroot.ErrOutsideRoot.Error(),
		unwrapTarget(missingErr, filepath.Join(s.root, "absent.pdf")))
	s.formats.AssertNotCalled(s.T(), "Supports", mock.Anything)
	s.reader.AssertNotCalled(s.T(), "Read", mock.Anything, mock.Anything)
}

// A file inside a root is inspected.
func (s *InspectSuite) TestAFileInsideARootIsInspected() {
	path := filepath.Join(s.root, "guide.md")
	s.Require().NoError(os.WriteFile(path, []byte("# Guide\n"), 0o600))
	s.formats.EXPECT().Supports(path).Return(true)

	report, err := s.service.Inspect(s.T().Context(), path)
	s.Require().NoError(err)
	s.Equal("markup", report.PredictedSource)
}

// unwrapTarget is the error with the target it names taken off the front.
func unwrapTarget(err error, target string) string {
	return err.Error()[len(target+": "):]
}
