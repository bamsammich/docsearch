package connectapi

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/bamsammich/docsearch/internal/service/sharing"
)

// Each refusal the database makes about sharing answers with the Connect code
// a client can act on, and anything else is an internal error.
func TestSharingRefusalsAnswerWithTheirCodes(t *testing.T) {
	for refusal, want := range map[error]connect.Code{
		sharing.ErrNoLibrary:                            connect.CodeNotFound,
		sharing.ErrNoAccount:                            connect.CodeNotFound,
		sharing.ErrLastAdmin:                            connect.CodeFailedPrecondition,
		sharing.ErrNameTaken:                            connect.CodeAlreadyExists,
		sharing.ErrInvalid:                              connect.CodeInvalidArgument,
		fmt.Errorf("wrapped: %w", sharing.ErrNoAccount): connect.CodeNotFound,
		errors.New("the database went away"):            connect.CodeInternal,
	} {
		if got := connect.CodeOf(sharingError(refusal)); got != want {
			t.Errorf("%v answered %v, want %v", refusal, got, want)
		}
	}
}
