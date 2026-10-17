package owner_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/bamsammich/docsearch/internal/owner"
)

type OwnerSuite struct{ suite.Suite }

func TestOwner(t *testing.T) { suite.Run(t, new(OwnerSuite)) }

func (s *OwnerSuite) TestMiddlewareNamesTheUserAHandlerReads() {
	var got string
	var readErr error
	// Read inside the handler, asserted outside it: a failed assertion stops
	// the goroutine it runs on, which is not the test's own.
	handler := owner.Middleware(owner.Builtin,
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got, readErr = owner.From(r.Context())
		}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	s.Require().NoError(readErr)
	s.Equal(owner.Builtin, got)
}

// The case the package exists for: a handler reached without the middleware
// is refused, because the alternative is reading the built-in user's library
// for whoever asked.
func (s *OwnerSuite) TestAContextWithNoOwnerIsRefused() {
	_, err := owner.From(context.Background())
	s.Require().ErrorIs(err, owner.ErrNoOwner)
}

func (s *OwnerSuite) TestAnEmptyOwnerIsRefused() {
	// An empty string reaches the database as a session that names no user,
	// which matches no rows, so it is refused where it can still be
	// explained.
	_, err := owner.From(owner.With(context.Background(), ""))
	s.Require().ErrorIs(err, owner.ErrNoOwner)
}
