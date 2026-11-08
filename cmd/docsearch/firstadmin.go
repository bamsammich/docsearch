package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/password/pgpassword"
	"github.com/bamsammich/docsearch/internal/repository/postgres"
)

const (
	flagAdminUsername = "admin-username"
	flagAdminPassword = "admin-password" //nolint:gosec // name of a flag, not a credential
)

var errHalfAnAdmin = fmt.Errorf("set both %s and %s, or neither",
	config.EnvAdminUsername, config.EnvAdminPassword)

// firstAdmin is the administrator a deployment names in its environment, the
// way Miniflux and Paperless-ngx make theirs, so a password-mode deployment
// needs no command run by hand before someone can sign in and manage it.
//
// migrate creates it because migrate already holds the owner credential and
// runs before the server on every start; the server never holds that
// credential.
type firstAdmin struct {
	Username string
	Secret   string
}

// firstAdminOf reads the first administrator from the migrate command. An
// empty Username means none was named.
func firstAdminOf(cmd *cli.Command) (firstAdmin, error) {
	first := firstAdmin{
		Username: strings.ToLower(cmd.String(flagAdminUsername)),
		Secret:   cmd.String(flagAdminPassword),
	}
	if (first.Username == "") != (first.Secret == "") {
		return firstAdmin{}, errHalfAnAdmin
	}
	return first, nil
}

// Create adds the account and makes it an administrator, reporting false and
// changing nothing when an account already holds the username. Leaving an
// existing account alone is what makes running it on every start safe: a
// password changed since, or the role taken away, stays as it was.
func (f firstAdmin) Create(ctx context.Context, owner *sql.DB) (bool, error) {
	accounts, err := password.New(pgpassword.New(owner), time.Now)
	if err != nil {
		return false, err
	}
	if err := accounts.Add(ctx, f.Username, f.Secret, false); err != nil {
		if errors.Is(err, password.ErrTaken) {
			return false, nil
		}
		return false, fmt.Errorf("create the first administrator: %w", err)
	}
	if _, err := postgres.GrantAdmin(ctx, owner, f.Username, true); err != nil {
		return false, fmt.Errorf("%w; run `docsearch account admin %s`", err, f.Username)
	}
	return true, nil
}

// Report creates the first administrator, when one was named, and says
// whether it did.
func (f firstAdmin) Report(ctx context.Context, owner *sql.DB) error {
	if f.Username == "" {
		return nil
	}
	created, err := f.Create(ctx, owner)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("created %s, an administrator\n", f.Username)
	} else {
		fmt.Printf("%s already has an account; left as it is\n", f.Username)
	}
	return nil
}
