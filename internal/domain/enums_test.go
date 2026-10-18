package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/domain"
)

// EnumSuite covers the text the three enums persist as, which v1 databases
// already hold and the Python pipeline still writes.
type EnumSuite struct{ suite.Suite }

func TestEnums(t *testing.T) { suite.Run(t, new(EnumSuite)) }

func (s *EnumSuite) TestAGradePersistsAsTheTextV1Stores() {
	for value, text := range map[domain.Quality]string{
		domain.QualityOK:       "ok",
		domain.QualityDegraded: "degraded",
		domain.QualityFailed:   "failed",
	} {
		s.Equal(text, value.String())
		raw, err := json.Marshal(value)
		s.Require().NoError(err)
		s.JSONEq(`"`+text+`"`, string(raw))
	}
}

func (s *EnumSuite) TestEverySourceAnAdapterCanReportHasText() {
	// The set is closed, so a source added without text would marshal as an
	// error at ingest rather than at compile time. Walking the range catches
	// the gap here instead.
	for value := domain.SourceOutline; value <= domain.SourceUnknown; value++ {
		raw, err := value.MarshalText()
		s.Require().NoError(err, "%d has no text", value)
		parsed, err := domain.ParseStructureSource(string(raw))
		s.Require().NoError(err)
		s.Equal(value, parsed)
	}
}

func (s *EnumSuite) TestTheTextMatchesWhatPythonWrites() {
	s.Equal("none (blank-line paragraphs)", domain.SourceBlankLines.String())
	s.Equal("h1_h6_nesting", domain.SourceHTMLNesting.String())
	s.Equal("keyword-reference", domain.KindKeywordReference.String())
}

func (s *EnumSuite) TestAnUnsetValueRefusesToPersist() {
	// Zero means nobody assigned a value. Writing it as text would put a
	// grade in the database that no code chose.
	var unset domain.Quality
	_, err := unset.MarshalText()
	s.Require().Error(err)
	s.Contains(err.Error(), "unknown quality 0")
}

func (s *EnumSuite) TestAnUnknownTextIsRefusedAndNamed() {
	_, err := domain.ParseStructureSource("vibes")
	s.Require().Error(err)
	s.Contains(err.Error(), `unknown structure source "vibes"`)
}

func (s *EnumSuite) TestAChunkRoundTripsItsKind() {
	raw, err := json.Marshal(domain.Chunk{Kind: domain.KindKeywordReference})
	s.Require().NoError(err)
	s.Contains(string(raw), `"kind":"keyword-reference"`)

	var back domain.Chunk
	s.Require().NoError(json.Unmarshal(raw, &back))
	s.Equal(domain.KindKeywordReference, back.Kind)
}

func (s *EnumSuite) TestAnUnnamedValuePrintsItsNumber() {
	// Reached only by a failure message, since marshalling refuses the value.
	s.Equal("chunk kind(9)", domain.ChunkKind(9).String())
}
