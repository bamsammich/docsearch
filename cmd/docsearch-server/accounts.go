package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/bamsammich/docsearch/internal/api/connectapi"
	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/password/pgpassword"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
	"github.com/bamsammich/docsearch/internal/service/admin"
	"github.com/bamsammich/docsearch/internal/service/settings"
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
		func(user string) (connectapi.Settings, error) {
			return settings.New(editable, cfg.Origins, cfg.File,
				postgres.NewSettings(db, user), os.ReadFile), nil
		},
		cfg.Auth.String(),
		signInSettings(cfg),
		cfg.File,
		providerAdminGroup(cfg),
	)), nil
}

// providerAdminGroup is the provider group administrators come from: only
// in oidc mode, and only once docsearch reads the provider's groups.
func providerAdminGroup(cfg config.Config) string {
	if cfg.Auth != owner.OIDC || cfg.OIDC.GroupsClaim == "" {
		return ""
	}
	return cfg.OIDC.AdminGroup
}

// passwordAccounts are password mode's accounts as an administrator changes
// them: the same operations `docsearch account` runs with the owner DSN. The
// context names the administrator, and the database refuses anyone else.
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

// Remove hands the removed account's libraries to heir, the administrator
// removing it. The store records the user the context names as the one who
// did.
func (p passwordAccounts) Remove(ctx context.Context, username, heir string) error {
	return p.store.Remove(ctx, username, password.Handover{To: heir})
}

func (p passwordAccounts) List(ctx context.Context) ([]password.Account, error) {
	return p.store.List(ctx)
}

// signInSettings are how people sign in, as the configuration sets it, for
// an administrator to read and change: each value, where it came from, and
// the flag that sets it. A rule the server keeps rather than one it is
// configured with names no flag.
func signInSettings(cfg config.Config) []connectapi.Setting {
	// Who may create groups is shown on Administration › Groups, beside the
	// groups; it travels with the sign-in settings because the same call
	// changes it.
	return append(signInOnly(cfg),
		configured(cfg, "Who can create groups", cfg.GroupCreators, flagCreators,
			config.EnvGroupCreators))
}

// signInOnly are how people sign in, for the Sign-in page.
func signInOnly(cfg config.Config) []connectapi.Setting {
	method := configured(cfg, "Sign-in", "", "auth", config.EnvAuth)
	switch cfg.Auth {
	case owner.Password:
		method.Value = "Accounts docsearch holds, with passwords"
		return []connectapi.Setting{
			method,
			configured(cfg, "Sign-in page", cfg.PublicURL, flagPublicURL, config.EnvPublicURL),
			{
				Label: "Failed sign-ins",
				Value: fmt.Sprintf("%d in a row lock an account for %d minutes",
					password.LockThreshold, int(password.LockDuration.Minutes())),
			},
			{Label: "New people", Value: "Only an administrator adds them"},
		}
	case owner.OIDC:
		method.Value = "Your OIDC provider"
		return []connectapi.Setting{
			method,
			configured(cfg, "Provider", cfg.OIDC.Issuer, flagOIDCIssuer, config.EnvOIDCIssuer),
			configured(cfg, "Claim every token must carry", cfg.OIDC.RequiredClaimName,
				flagClaimName, config.EnvOIDCRequiredClaimName),
			configured(cfg, "Value that claim must hold", cfg.OIDC.RequiredClaimValue,
				flagClaimValue, config.EnvOIDCRequiredClaimValue),
			configured(cfg, "Create an account and a personal library on first sign-in",
				strconv.FormatBool(cfg.OIDC.SignUp), flagSignUp, config.EnvOIDCSignUp),
			configured(cfg, "Usernames come from", usernameSource(cfg.OIDC.UsernameClaim),
				flagUsername, config.EnvOIDCUsernameClaim),
			configured(cfg, "Claim listing provider groups", cfg.OIDC.GroupsClaim,
				flagGroupsClaim, config.EnvOIDCGroupsClaim),
			configured(cfg, "Administrators come from the provider group", cfg.OIDC.AdminGroup,
				flagAdminGroup, config.EnvOIDCAdminGroup),
			configured(cfg, "Create a group for each provider group",
				strconv.FormatBool(cfg.OIDC.GroupAutoCreate), flagAutoCreate,
				config.EnvOIDCGroupAutoCreate),
			configured(cfg, "Provider groups that get a group", cfg.OIDC.GroupFilter,
				flagGroupFilter, config.EnvOIDCGroupFilter),
		}
	case owner.None:
		method.Value = "No sign-in: local access only"
	default:
		method.Value = "The server token"
	}
	return []connectapi.Setting{method}
}

// usernameSource is where OIDC usernames come from, as an administrator
// reads it.
func usernameSource(claim string) string {
	if claim != "" {
		return "The " + claim + " claim"
	}
	return "A verified email, else preferred_username"
}

// configured is a setting flag makes, with where its value came from, and
// whether the library UI changes it.
func configured(cfg config.Config, label, value, flag, env string) connectapi.Setting {
	e, editable := editable[flag]
	return connectapi.Setting{
		Label:    label,
		Value:    value,
		Name:     flag,
		Flag:     "--" + flag,
		Env:      env,
		Origin:   cfg.Origins[flag],
		Editable: editable,
		Boolean:  e.Boolean,
	}
}
