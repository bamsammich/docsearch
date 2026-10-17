package owner_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/owner"
)

type ModeSuite struct{ suite.Suite }

func TestMode(t *testing.T) { suite.Run(t, new(ModeSuite)) }

func (s *ModeSuite) TestTheBuiltModesParse() {
	for spelling, want := range map[string]owner.Mode{
		"none":     owner.None,
		"token":    owner.Token,
		"oidc":     owner.OIDC,
		"password": owner.Password,
	} {
		got, err := owner.ParseMode(spelling)
		s.Require().NoError(err, spelling)
		s.Equal(want, got, spelling)
		s.Equal(spelling, got.String(), "a mode reports back the spelling it was written as")
	}
}

func (s *ModeSuite) TestAnUnknownModeListsTheOnesThatExist() {
	_, err := owner.ParseMode("basic")
	s.Require().Error(err)
	s.Contains(err.Error(), "token", "the message names what an operator could write instead")
}

// The zero value is not a mode, and above all not the one that asks for no
// credential. A Mode nobody parsed must not read as none.
func (s *ModeSuite) TestTheZeroValueIsNotAMode() {
	var unset owner.Mode
	s.NotEqual(owner.None, unset)
	s.Contains(unset.String(), "Mode(0)")
}
