// Package password holds the accounts a deployment in password mode signs
// in, for one that has no identity provider.
//
// Holding a password is the part of password mode that docsearch is no
// better at than a provider, so this package does only what that requires.
// Hashes are argon2id. An account locks for a while after repeated failures,
// so a guessed password costs an attacker hours, not seconds. A refused
// sign-in never says which part was wrong, so the page cannot be used to
// learn which usernames exist. An operator resets a password through
// `docsearch account`; there is no self-service reset, because that needs a
// mail server and an address on file, and a small deployment has neither.
package password

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
)

// The lockout policy: this many failures in a row lock an account for this
// long. A successful sign-in, or an operator setting a new password, clears
// the count.
const (
	LockThreshold = 5
	LockDuration  = 15 * time.Minute
)

// MinLength is the shortest password an account may have. Length is the one
// rule that measurably helps, and NIST's guidance asks for nothing else.
const MinLength = 12

// Builtin is the account the library from before accounts belongs to.
const Builtin = "default"

var (
	// ErrSignInRefused is every failed sign-in, whatever the cause.
	ErrSignInRefused = errors.New("wrong username or password, or the account is " +
		"locked after too many attempts; try again later or ask whoever runs this server")
	// ErrNoSuchAccount is an operator naming an account that does not exist.
	ErrNoSuchAccount = errors.New("no account with that username")
	// ErrBuiltinTaken is a second account trying to take the built-in library.
	ErrBuiltinTaken = errors.New("the library from before accounts already has an owner")
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

// params are the argon2id costs: the library's defaults, which follow RFC
// 9106's second recommendation for memory-constrained machines.
var params = argon2id.DefaultParams

// Account is one password account, as an operator lists it.
type Account struct {
	CreatedAt   time.Time
	LockedUntil time.Time
	Username    string
	UserID      string
	Failures    int
}

// Record is an account with what checking a password needs.
type Record struct {
	Hash string
	Account
}

// Store is where accounts live.
type Store interface {
	// Add creates the account and the library it signs in to. builtin
	// gives it the library from before accounts instead of a new one.
	Add(ctx context.Context, username, userID, hash string, builtin bool) error
	Find(ctx context.Context, username string) (Record, bool, error)
	Failed(ctx context.Context, username string, threshold int, lockUntil time.Time) error
	Succeeded(ctx context.Context, username string) error
	SetHash(ctx context.Context, username, hash string) error
	// Remove ends the account's sign-ins. Its library stays, since what
	// happens to a library when its account goes is not settled.
	Remove(ctx context.Context, username string) error
	List(ctx context.Context) ([]Account, error)
	// Exists reports whether a user still has a password account, so a
	// removed account's tokens stop working at once.
	Exists(ctx context.Context, userID string) (bool, error)
}

// Accounts applies the sign-in policy over a store.
type Accounts struct {
	store Store
	now   func() time.Time
	// decoy is checked when a username does not exist, so a refusal takes
	// as long whether or not the name is real.
	decoy string
}

// New returns the accounts in store.
func New(store Store, now func() time.Time) (*Accounts, error) {
	decoy, err := argon2id.CreateHash(rand.Text(), params)
	if err != nil {
		return nil, fmt.Errorf("prepare sign-in: %w", err)
	}
	return &Accounts{store: store, now: now, decoy: decoy}, nil
}

// Add creates an account. builtin makes it the owner of the library from
// before accounts.
func (a *Accounts) Add(ctx context.Context, username, secret string, builtin bool) error {
	if err := checkUsername(username); err != nil {
		return err
	}
	hash, err := hashOf(secret)
	if err != nil {
		return err
	}
	userID := Builtin
	if !builtin {
		userID = strings.ToLower(rand.Text())
	}
	return a.store.Add(ctx, username, userID, hash, builtin)
}

// SetPassword replaces an account's password and lifts any lock.
func (a *Accounts) SetPassword(ctx context.Context, username, secret string) error {
	hash, err := hashOf(secret)
	if err != nil {
		return err
	}
	return a.store.SetHash(ctx, username, hash)
}

// Authenticate is the user a username and password sign in as.
func (a *Accounts) Authenticate(ctx context.Context, username, secret string) (string, error) {
	record, found, err := a.store.Find(ctx, strings.ToLower(username))
	if err != nil {
		return "", err
	}
	if !found {
		return "", a.refuseUnknown(secret)
	}
	if a.now().Before(record.LockedUntil) {
		return "", ErrSignInRefused
	}
	return a.check(ctx, record, secret)
}

// refuseUnknown refuses a username that does not exist. The comparison is
// the expensive part, so a missing name runs it too and the refusal takes the
// same time.
func (a *Accounts) refuseUnknown(secret string) error {
	if _, err := argon2id.ComparePasswordAndHash(secret, a.decoy); err != nil {
		return fmt.Errorf("check password: %w", err)
	}
	return ErrSignInRefused
}

// check compares the password, counting a failure towards the lock.
func (a *Accounts) check(ctx context.Context, record Record, secret string) (string, error) {
	match, err := argon2id.ComparePasswordAndHash(secret, record.Hash)
	if err != nil {
		return "", fmt.Errorf("check password: %w", err)
	}
	if !match {
		if err := a.store.Failed(ctx, record.Username, LockThreshold,
			a.now().Add(LockDuration)); err != nil {
			return "", err
		}
		return "", ErrSignInRefused
	}
	if err := a.store.Succeeded(ctx, record.Username); err != nil {
		return "", err
	}
	return record.UserID, nil
}

// Generate is a password for an operator to hand on: 26 characters of
// base32, about 130 bits.
func Generate() string { return rand.Text() }

func checkUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return fmt.Errorf("username %q: use 1 to 64 lower-case letters, digits, '.', '_' or '-'",
			username)
	}
	return nil
}

func hashOf(secret string) (string, error) {
	if len([]rune(secret)) < MinLength {
		return "", fmt.Errorf("a password needs at least %d characters", MinLength)
	}
	hash, err := argon2id.CreateHash(secret, params)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return hash, nil
}
