package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/password"
	"github.com/bamsammich/docsearch/internal/password/pgpassword"
)

// argUsername is the argument every account subcommand but list takes.
const argUsername = "<username>"

var (
	errNoUsername      = errors.New("name the account's username")
	errPasswordsDiffer = errors.New("the two passwords do not match")
	errNoTerminal      = errors.New("no terminal to read a password from: pass --generate " +
		"or --password-stdin")
)

// accountCommand administers password mode's accounts.
//
// It reaches the database as its owner, the same credential migrate uses,
// so whoever can administer the schema can administer accounts and nobody
// else can: there is no admin role inside docsearch to grant or leak.
func accountCommand() *cli.Command {
	dsn := &cli.StringFlag{
		Name:    "dsn",
		Usage:   "Postgres `DSN`, as the database owner",
		Sources: cli.EnvVars(config.EnvDSN),
	}
	secret := []cli.Flag{
		&cli.BoolFlag{Name: "generate", Usage: "make up a password and print it once"},
		&cli.BoolFlag{Name: "password-stdin", Usage: "read the password from standard input"},
	}
	return &cli.Command{
		Name:  "account",
		Usage: "manage the accounts password mode signs in",
		Commands: []*cli.Command{
			{
				Name: "add", Usage: "create an account and its library", ArgsUsage: argUsername,
				Flags: append([]cli.Flag{dsn, &cli.BoolFlag{
					Name:  "takes-builtin",
					Usage: "give it the library this deployment held before it had accounts",
				}}, secret...),
				Action: withAccounts(addAccount),
			},
			{
				Name:      "passwd",
				Usage:     "set an account's password, and unlock it",
				ArgsUsage: argUsername,
				Flags:     append([]cli.Flag{dsn}, secret...),
				Action:    withAccounts(setPassword),
			},
			{
				Name:      "unlock",
				Usage:     "clear an account's failed sign-ins",
				ArgsUsage: argUsername,
				Flags:     []cli.Flag{dsn},
				Action:    withAccounts(unlockAccount),
			},
			{
				Name:      "remove",
				Usage:     "end an account's sign-ins; its library stays",
				ArgsUsage: argUsername,
				Flags:     []cli.Flag{dsn},
				Action:    withAccounts(removeAccount),
			},
			{
				Name: "list", Usage: "show every account", Flags: []cli.Flag{dsn},
				Action: withAccounts(listAccounts),
			},
		},
	}
}

// accountAction is one account subcommand, given the store and the policy.
type accountAction func(ctx context.Context, cmd *cli.Command, store *pgpassword.Store,
	accounts *password.Accounts) error

func withAccounts(action accountAction) cli.ActionFunc {
	return func(ctx context.Context, cmd *cli.Command) error {
		dsn := cmd.String("dsn")
		if dsn == "" {
			return fmt.Errorf("no database: pass --dsn or set %s", config.EnvDSN)
		}
		db, err := open(dsn)
		if err != nil {
			return err
		}
		defer func(db *sql.DB) { _ = db.Close() }(db)
		store := pgpassword.New(db)
		accounts, err := password.New(store, time.Now)
		if err != nil {
			return err
		}
		return action(ctx, cmd, store, accounts)
	}
}

func usernameOf(cmd *cli.Command) (string, error) {
	username := strings.ToLower(cmd.Args().First())
	if username == "" {
		return "", errNoUsername
	}
	return username, nil
}

func addAccount(ctx context.Context, cmd *cli.Command, _ *pgpassword.Store,
	accounts *password.Accounts) error {
	username, err := usernameOf(cmd)
	if err != nil {
		return err
	}
	secret, err := readSecret(cmd)
	if err != nil {
		return err
	}
	if err := accounts.Add(ctx, username, secret, cmd.Bool("takes-builtin")); err != nil {
		return err
	}
	fmt.Printf("added %s\n", username)
	return nil
}

func setPassword(ctx context.Context, cmd *cli.Command, _ *pgpassword.Store,
	accounts *password.Accounts) error {
	username, err := usernameOf(cmd)
	if err != nil {
		return err
	}
	secret, err := readSecret(cmd)
	if err != nil {
		return err
	}
	if err := accounts.SetPassword(ctx, username, secret); err != nil {
		return err
	}
	fmt.Printf("password set for %s\n", username)
	return nil
}

func unlockAccount(ctx context.Context, cmd *cli.Command, store *pgpassword.Store,
	_ *password.Accounts) error {
	username, err := usernameOf(cmd)
	if err != nil {
		return err
	}
	if _, found, err := store.Find(ctx, username); err != nil || !found {
		return errors.Join(err, errIf(!found, password.ErrNoSuchAccount))
	}
	if err := store.Succeeded(ctx, username); err != nil {
		return err
	}
	fmt.Printf("unlocked %s\n", username)
	return nil
}

func removeAccount(ctx context.Context, cmd *cli.Command, store *pgpassword.Store,
	_ *password.Accounts) error {
	username, err := usernameOf(cmd)
	if err != nil {
		return err
	}
	if err := store.Remove(ctx, username); err != nil {
		return err
	}
	fmt.Printf("removed %s; its library is kept\n", username)
	return nil
}

func listAccounts(ctx context.Context, _ *cli.Command, store *pgpassword.Store,
	_ *password.Accounts) error {
	list, err := store.List(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%-24s %-28s %s\n", "USERNAME", "LIBRARY", "STATE")
	for _, a := range list {
		state := "active"
		if time.Now().Before(a.LockedUntil) {
			state = "locked until " + a.LockedUntil.Local().Format(time.Kitchen)
		}
		fmt.Printf("%-24s %-28s %s\n", a.Username, a.UserID, state)
	}
	return nil
}

// errIf is err when cond holds, and nil otherwise.
func errIf(cond bool, err error) error {
	if cond {
		return err
	}
	return nil
}

// readSecret takes the password from where the operator said: made up and
// printed once, read from standard input for a script, or typed twice at a
// terminal that does not echo it.
func readSecret(cmd *cli.Command) (string, error) {
	switch {
	case cmd.Bool("generate"):
		secret := password.Generate()
		fmt.Printf("password: %s\n", secret)
		return secret, nil
	case cmd.Bool("password-stdin"):
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read password: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	default:
		return typedTwice()
	}
}

func typedTwice() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errNoTerminal
	}
	first, err := prompt(fd, "password: ")
	if err != nil {
		return "", err
	}
	second, err := prompt(fd, "again: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errPasswordsDiffer
	}
	return first, nil
}

func prompt(fd int, label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	typed, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(typed), nil
}
