package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bamsammich/docsearch/internal/account"
	"github.com/bamsammich/docsearch/internal/account/pgaccount"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/resource"
	"github.com/bamsammich/docsearch/internal/resource/introspect"
	"github.com/bamsammich/docsearch/internal/resource/signed"
)

// discoveryTimeout bounds asking the provider for its endpoints and keys at
// startup. A provider that does not answer stops the server from starting,
// which is better than a server that refuses every request.
const discoveryTimeout = 30 * time.Second

// access is how a deployment's mode guards its routes.
type access struct {
	guard func(http.Handler) http.Handler
	// public are served with no credential at all: the metadata that tells
	// a client where to get one, in oidc mode.
	public []route
}

// newAccess is the guard for the configured mode.
func newAccess(cfg config.Config, db *sql.DB, log *slog.Logger) (access, error) {
	if cfg.Auth != owner.OIDC {
		return access{guard: func(next http.Handler) http.Handler {
			return authenticated(cfg, next)
		}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()
	readers, err := discover(ctx, cfg.OIDC)
	if err != nil {
		return access{}, err
	}
	return oidcAccess(cfg.OIDC, readers, pgaccount.New(db), log)
}

// readers are the two ways a token is read, found at the provider.
type readers struct {
	signed resource.Reader
	opaque resource.Reader
}

// discover asks the provider for what both readers need. Introspection is
// set up only when docsearch has credentials to introspect with.
func discover(ctx context.Context, cfg config.OIDC) (readers, error) {
	jwt, err := signed.Discover(ctx, cfg.Issuer)
	if err != nil {
		return readers{}, err
	}
	found := readers{signed: jwt, opaque: resource.Unintrospected{}}
	if cfg.ClientID == "" {
		return found, nil
	}
	found.opaque, err = introspect.Discover(ctx, cfg.Issuer, cfg.ClientID, cfg.ClientSecret)
	if err != nil {
		return readers{}, err
	}
	return found, nil
}

// oidcAccess puts every route behind a token the provider issued for
// docsearch, then behind an account for the identity it names.
func oidcAccess(
	cfg config.OIDC,
	found readers,
	store account.Store,
	log *slog.Logger,
) (access, error) {
	server, err := resource.New(resource.Config{
		Issuer:          cfg.Issuer,
		Resource:        cfg.Resource,
		Audience:        cfg.Audience,
		ScopesSupported: cfg.ScopesSupported,
		RequiredScopes:  cfg.RequiredScopes,
	}, found.signed, found.opaque, log)
	if err != nil {
		return access{}, fmt.Errorf("auth mode oidc: %w", err)
	}
	accounts := account.New(store, account.Policy{
		DefaultOwner: defaultOwner(cfg),
		Required: account.RequiredClaim{
			Name: cfg.RequiredClaimName, Value: cfg.RequiredClaimValue,
		},
		SignUp: cfg.SignUp,
	}, log)
	return access{
		guard: func(next http.Handler) http.Handler {
			return server.Protect(accounts.Middleware(next))
		},
		public: []route{{path: server.MetadataPath(), handler: server.Metadata()}},
	}, nil
}

// defaultOwner is the configured subject at the configured issuer. A subject
// alone is the setting because the issuer is already fixed: an oidc
// deployment trusts one provider.
func defaultOwner(cfg config.OIDC) resource.Identity {
	if cfg.DefaultOwner == "" {
		return resource.Identity{}
	}
	return resource.Identity{Issuer: cfg.Issuer, Subject: cfg.DefaultOwner}
}
