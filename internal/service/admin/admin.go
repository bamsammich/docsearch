// Package admin is what a docsearch administrator does: see every account,
// add and remove password accounts, reset and unlock them, and choose who
// else administers.
//
// The role covers accounts and nothing else. An administrator reads a
// library only through a grant on it, like anyone, so nothing here touches a
// library's rows.
package admin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bamsammich/docsearch/internal/password"
)

// Actions an administrator's change to a password account is recorded as.
const (
	ActionAdd    = "add"
	ActionReset  = "reset"
	ActionUnlock = "unlock"
	ActionRemove = "remove"
)

var (
	// ErrNotAdmin is anyone but an administrator asking.
	ErrNotAdmin = errors.New("only a docsearch administrator can do that")
	// ErrNoAccount is a username no account holds.
	ErrNoAccount = errors.New("no account by that username")
	// ErrLastAdmin is a change that would leave nobody administering.
	ErrLastAdmin = errors.New(
		"that would leave nobody to administer docsearch: make someone else an administrator first")
	// ErrNoPasswords is a password action on a docsearch that signs people
	// in through a provider, where accounts are added and removed.
	ErrNoPasswords = errors.New(
		"this docsearch signs people in through its provider: add and remove them there")
	// ErrSelf is an administrator removing their own account, which would
	// end the session they are using.
	ErrSelf = errors.New("you cannot remove your own account: ask another administrator")
)

// Account is one account as an administrator sees it.
type Account struct {
	CreatedAt time.Time
	// LockedUntil is when a locked password account can try again, and
	// zero for one that is not locked.
	LockedUntil time.Time
	// Username is empty for an account that has not signed in since
	// usernames were recorded.
	Username string
	Admin    bool
	// Password is true for an account that signs in with a docsearch
	// password, which an administrator can reset, unlock and remove.
	Password bool
}

// Me is the account asking.
type Me struct {
	Username string
	Admin    bool
}

// Store is the accounts table as one user reaches it. The database refuses
// a change from anyone but an administrator, and records each one.
type Store interface {
	Me(ctx context.Context) (Me, error)
	List(ctx context.Context) ([]Account, error)
	SetAdmin(ctx context.Context, username string, admin bool) error
	Record(ctx context.Context, action, username string) error
}

// Passwords are the accounts password mode signs in.
type Passwords interface {
	Add(ctx context.Context, username, secret string) error
	SetPassword(ctx context.Context, username, secret string) error
	Unlock(ctx context.Context, username string) error
	Remove(ctx context.Context, username string) error
	List(ctx context.Context) ([]password.Account, error)
}

// Service is the administrator's work, as one user.
type Service struct {
	store Store
	// passwords is nil where docsearch holds no passwords.
	passwords Passwords
	generate  func() string
	now       func() time.Time
}

// New builds the service. passwords is nil in any mode but password.
func New(store Store, passwords Passwords, generate func() string, now func() time.Time) *Service {
	return &Service{store: store, passwords: passwords, generate: generate, now: now}
}

// Me is the account asking, and whether it administers.
func (s *Service) Me(ctx context.Context) (Me, error) {
	return s.store.Me(ctx)
}

// List is every account. In password mode an account whose sign-in was
// removed is left out, since nobody can use it.
func (s *Service) List(ctx context.Context) ([]Account, error) {
	if _, err := s.admin(ctx); err != nil {
		return nil, err
	}
	accounts, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	if s.passwords == nil {
		return accounts, nil
	}
	held, err := s.passwords.List(ctx)
	if err != nil {
		return nil, err
	}
	return signingIn(accounts, held, s.now()), nil
}

// signingIn is accounts narrowed to those with a password sign-in, each
// marked with its lock while one holds.
func signingIn(accounts []Account, held []password.Account, now time.Time) []Account {
	out := make([]Account, 0, len(held))
	for _, account := range accounts {
		i := slices.IndexFunc(held, func(p password.Account) bool {
			return p.Username == account.Username
		})
		if i < 0 {
			continue
		}
		account.Password = true
		if held[i].LockedUntil.After(now) {
			account.LockedUntil = held[i].LockedUntil
		}
		out = append(out, account)
	}
	return out
}

// Add creates a password account and its library, and returns the password
// it was made with, which is shown once.
func (s *Service) Add(ctx context.Context, username string) (string, error) {
	if err := s.passwordAdmin(ctx); err != nil {
		return "", err
	}
	secret := s.generate()
	if err := s.passwords.Add(ctx, username, secret); err != nil {
		return "", err
	}
	return secret, s.store.Record(ctx, ActionAdd, username)
}

// Reset gives a password account a new password, lifts any lock, and
// returns the password, which is shown once.
func (s *Service) Reset(ctx context.Context, username string) (string, error) {
	if err := s.passwordAdmin(ctx); err != nil {
		return "", err
	}
	secret := s.generate()
	if err := s.passwords.SetPassword(ctx, username, secret); err != nil {
		return "", err
	}
	return secret, s.store.Record(ctx, ActionReset, username)
}

// Unlock clears a password account's failed sign-ins.
func (s *Service) Unlock(ctx context.Context, username string) error {
	if err := s.passwordAdmin(ctx); err != nil {
		return err
	}
	if err := s.passwords.Unlock(ctx, username); err != nil {
		return err
	}
	return s.store.Record(ctx, ActionUnlock, username)
}

// Remove ends a password account's sign-in and every token it holds. Its
// libraries stay, and so does every library it was sharing.
func (s *Service) Remove(ctx context.Context, username string) error {
	me, err := s.admin(ctx)
	if err != nil {
		return err
	}
	if s.passwords == nil {
		return ErrNoPasswords
	}
	if username == me.Username {
		return ErrSelf
	}
	if err := s.passwords.Remove(ctx, username); err != nil {
		return err
	}
	return s.store.Record(ctx, ActionRemove, username)
}

// SetAdmin makes an account an administrator, or stops it being one.
func (s *Service) SetAdmin(ctx context.Context, username string, admin bool) error {
	if _, err := s.admin(ctx); err != nil {
		return err
	}
	return s.store.SetAdmin(ctx, username, admin)
}

// admin is the account asking, refused unless it administers.
func (s *Service) admin(ctx context.Context) (Me, error) {
	me, err := s.store.Me(ctx)
	if err != nil {
		return Me{}, fmt.Errorf("check who is asking: %w", err)
	}
	if !me.Admin {
		return Me{}, ErrNotAdmin
	}
	return me, nil
}

func (s *Service) passwordAdmin(ctx context.Context) error {
	if _, err := s.admin(ctx); err != nil {
		return err
	}
	if s.passwords == nil {
		return ErrNoPasswords
	}
	return nil
}
