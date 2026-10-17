package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/bamsammich/docsearch/internal/authserver"
	"github.com/bamsammich/docsearch/internal/authserver/pgauth"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/password/pgpassword"
	"github.com/bamsammich/docsearch/internal/resource"
	"github.com/bamsammich/docsearch/internal/resource/signed"
)

// signInAccounts is who may sign in: the password policy, and whether an
// account still exists.
type signInAccounts struct {
	*password.Accounts
	store *pgpassword.Store
}

func (a signInAccounts) Exists(ctx context.Context, userID string) (bool, error) {
	return a.store.Exists(ctx, userID)
}

// passwordAccess makes docsearch its own authorization server, and puts
// every route behind a token it issued.
//
// The resource server is the one oidc mode uses, verifying against
// docsearch's own keys rather than a provider's. Tokens name the subject as
// the user, so the guard after it only checks the account still exists.
func passwordAccess(cfg config.Config, db *sql.DB, log *slog.Logger) (access, error) {
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()
	issuer := strings.TrimSuffix(cfg.PublicURL, "/")
	resourceURI := issuer + "/mcp"

	store := pgpassword.New(db)
	accounts, err := password.New(store, time.Now)
	if err != nil {
		return access{}, err
	}
	server, err := authserver.New(ctx, authserver.Config{Issuer: issuer, Resource: resourceURI},
		pgauth.New(db), signInAccounts{Accounts: accounts, store: store}, log)
	if err != nil {
		return access{}, err
	}
	keys, err := server.KeySet(ctx)
	if err != nil {
		return access{}, err
	}
	protected, err := resource.New(resource.Config{
		Issuer:          issuer,
		Resource:        resourceURI,
		ScopesSupported: []string{authserver.Scope},
	}, signed.New(issuer, &oidc.StaticKeySet{PublicKeys: keys}), resource.Unintrospected{}, log)
	if err != nil {
		return access{}, fmt.Errorf("auth mode password: %w", err)
	}

	public := []route{{path: protected.MetadataPath(), handler: protected.Metadata()}}
	for _, r := range server.Routes() {
		public = append(public, route{path: r.Path, handler: r.Handler})
	}
	return access{
		guard: func(next http.Handler) http.Handler {
			return protected.Protect(server.Owner(next))
		},
		public: public,
	}, nil
}
