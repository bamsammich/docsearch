package domain_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

// PipelineSuite covers how a stored report says which pipeline wrote it,
// which is what decides whether held bytes are read again.
type PipelineSuite struct{ suite.Suite }

func TestPipeline(t *testing.T) { suite.Run(t, new(PipelineSuite)) }

func (s *PipelineSuite) TestAReportRecordsThePipelineThatWroteIt() {
	raw, err := domain.NewStructureReport(nil).JSON()
	s.Require().NoError(err)
	s.Equal(domain.PipelineVersion, domain.StoredPipelineVersion(string(raw)))
}

// Every report written before versions were recorded must read as older than
// the first recorded version, or nothing already in the index is re-read.
func (s *PipelineSuite) TestAReportWithNoVersionIsOlderThanAnyBuild() {
	stored := domain.StoredPipelineVersion(`{"quality":"degraded","notes":[]}`)
	s.Less(stored, domain.PipelineVersion)
}

func (s *PipelineSuite) TestAReportThatDoesNotDecodeIsOlderThanAnyBuild() {
	s.Zero(domain.StoredPipelineVersion(""))
	s.Zero(domain.StoredPipelineVersion("not json"))
}
