package authserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/resource"
)

// Owner names the user a verified request acts for. A token this server
// issued carries the user as its subject, so no lookup maps one to the
// other; what is checked is that the account still exists, so removing one
// stops its tokens at once rather than within the hour. Such a token is
// refused as invalid, 401, so a client signs in again rather than taking it
// for a request it may not make. It runs behind
// resource.Server.Protect, which already checked the issuer and audience.
func (s *Server) Owner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, err := resource.IdentityOf(auth.TokenInfoFromContext(r.Context()))
		if err != nil {
			s.log.ErrorContext(r.Context(), "owner middleware outside Protect", "err", err)
			http.Error(w, "no identity", http.StatusInternalServerError)
			return
		}
		present, err := s.accounts.Exists(r.Context(), identity.Subject)
		if err != nil {
			s.log.ErrorContext(r.Context(), "check account", "err", err)
			http.Error(w, "the account could not be checked", http.StatusInternalServerError)
			return
		}
		if !present {
			w.Header().Set("WWW-Authenticate",
				`Bearer error="invalid_token", error_description="this account no longer exists"`)
			http.Error(w, "this account no longer exists", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(owner.With(r.Context(), identity.Subject)))
	})
}
