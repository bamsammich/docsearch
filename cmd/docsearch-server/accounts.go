package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/bamsammich/docsearch/internal/api/connectapi"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/password/pgpassword"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/service/admin"
)

// newAccountRoute is the administrator's work, as the asking user. Only
// password mode has accounts docsearch can add, reset and remove; in every
// other mode those calls are refused, and an administrator still sees who
// signs in and how.
func newAccountRoute(cfg config.Config, db *sql.DB) (route, error) {
	var passwords admin.Passwords
	if cfg.Auth == owner.Password {
		store := pgpassword.New(db)
		accounts, err := password.New(store, time.Now)
		if err != nil {
			return route{}, err
		}
		passwords = passwordAccounts{accounts: accounts, store: store}
	}
	return mount(connectapi.NewAccountServer(
		func(user string) (connectapi.Admin, error) {
			return admin.New(
				postgres.NewAdmins(db, user),
				passwords,
				password.Generate,
				time.Now,
			), nil
		},
		cfg.Auth.String(),
		signInSettings(cfg),
		cfg.File,
	)), nil
}

// passwordAccounts are password mode's accounts as an administrator changes
// them: the same operations `docsearch account` runs with the owner DSN.
type passwordAccounts struct {
	accounts *password.Accounts
	store    *pgpassword.Store
}

func (p passwordAccounts) Add(ctx context.Context, username, secret string) error {
	return p.accounts.Add(ctx, username, secret, false)
}

func (p passwordAccounts) SetPassword(ctx context.Context, username, secret string) error {
	return p.accounts.SetPassword(ctx, username, secret)
}

func (p passwordAccounts) Unlock(ctx context.Context, username string) error {
	if _, found, err := p.store.Find(ctx, username); err != nil || !found {
		if err != nil {
			return err
		}
		return password.ErrNoSuchAccount
	}
	return p.store.Succeeded(ctx, username)
}

func (p passwordAccounts) Remove(ctx context.Context, username string) error {
	return p.store.Remove(ctx, username)
}

func (p passwordAccounts) List(ctx context.Context) ([]password.Account, error) {
	return p.store.List(ctx)
}

// signInSettings are how people sign in, as the configuration sets it, for
// an administrator to read: each value, where it came from, and the flag
// that sets it. A rule the server keeps rather than one it is configured
// with names no flag.
func signInSettings(cfg config.Config) []connectapi.Setting {
	method := configured(cfg, "Sign-in", "", "auth", config.EnvAuth)
	switch cfg.Auth {
	case owner.Password:
		method.Value = "Accounts docsearch holds, with passwords"
		return []connectapi.Setting{
			method,
			configured(cfg, "Sign-in page", cfg.PublicURL, "public-url", config.EnvPublicURL),
			{
				Label: "Failed sign-ins",
				Value: fmt.Sprintf("%d in a row lock an account for %d minutes",
					password.LockThreshold, int(password.LockDuration.Minutes())),
			},
			{Label: "New people", Value: "Only an administrator adds them"},
		}
	case owner.OIDC:
		method.Value = "Your OIDC provider"
		who := "Anyone the provider signs in"
		if cfg.OIDC.RequiredClaimName != "" {
			who = fmt.Sprintf("Tokens must carry %s = %s",
				cfg.OIDC.RequiredClaimName, cfg.OIDC.RequiredClaimValue)
		}
		joining := "Refused until an account exists for them"
		if cfg.OIDC.SignUp {
			joining = "Get an account and a personal library on their first sign-in"
		}
		return []connectapi.Setting{
			method,
			configured(cfg, "Provider", cfg.OIDC.Issuer, "oidc-issuer", config.EnvOIDCIssuer),
			configured(cfg, "Who may sign in", who,
				"oidc-required-claim-name", config.EnvOIDCRequiredClaimName),
			configured(cfg, "New people", joining, "oidc-sign-up", config.EnvOIDCSignUp),
		}
	case owner.None:
		method.Value = "No sign-in: local access only"
	default:
		method.Value = "The server token"
	}
	return []connectapi.Setting{method}
}

// configured is a setting flag makes, with where its value came from.
func configured(cfg config.Config, label, value, flag, env string) connectapi.Setting {
	return connectapi.Setting{
		Label:  label,
		Value:  value,
		Flag:   "--" + flag,
		Env:    env,
		Origin: cfg.Origins[flag],
	}
}
