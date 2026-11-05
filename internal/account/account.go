// Package account decides which library a verified identity reads.
//
// package resource has already decided the token is good and who it names.
// What is left is policy, and docsearch follows what self-hosted applications
// already do. Grafana and Gitea both pair a switch for creating an account on
// first sign-in with an optional claim every sign-in must carry, and both
// expect the provider to be the first gate. docsearch has the same two
// settings, and sign-up starts closed, as Gitea's does: an identity provider
// that lets anyone register would otherwise hand anyone a library.
package account

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/bamsammich/docsearch/internal/owner"
	"github.com/bamsammich/docsearch/internal/resource"
)

// Store is where accounts live.
type Store interface {
	// Find is the account an identity already has.
	Find(ctx context.Context, identity resource.Identity) (string, bool, error)
	// ClaimBuiltin hands the built-in library to identity, if nobody has
	// claimed it yet.
	ClaimBuiltin(ctx context.Context, identity resource.Identity) (string, bool, error)
	// Create makes an account and the partition its library lives in.
	Create(ctx context.Context, userID string, identity resource.Identity) error
	// Name records the username an account signed in under, and reports
	// whether the name is now the account's: one another account already
	// holds stays with that account.
	Name(ctx context.Context, userID, username string) (bool, error)
}

// RequiredClaim is a claim every token must carry. A claim that is a list
// passes when it holds Value, which is how a groups claim is spelled.
type RequiredClaim struct {
	Name  string
	Value string
}

// Policy is what an operator decided about who gets a library.
type Policy struct {
	// DefaultOwner inherits the built-in library, the one a deployment filled before
	// it had accounts. An empty Subject names nobody, and the library waits.
	DefaultOwner resource.Identity
	// Required is checked on every request, so revoking the claim at the
	// provider revokes access here. An empty Name requires nothing.
	Required RequiredClaim
	// SignUp creates an account for an identity that has none.
	SignUp bool
}

var (
	// ErrNoAccount is an identity with no account while sign-up is closed.
	ErrNoAccount = errors.New("no account for this identity, and sign-up is closed")
	// ErrNotAllowed is a token without the claim the policy requires.
	ErrNotAllowed = errors.New("the token does not carry the claim this server requires")
)

// Accounts resolves identities to libraries under one policy.
type Accounts struct {
	// named is each account whose username this process has recorded, and
	// the name it recorded, so a request writes the name only when it is new
	// or has changed.
	named  sync.Map
	store  Store
	log    *slog.Logger
	policy Policy
}

// New returns the resolver.
func New(store Store, policy Policy, log *slog.Logger) *Accounts {
	return &Accounts{store: store, policy: policy, log: log}
}

// Resolve is the user an identity acts as, creating the account when the
// policy allows it.
func (a *Accounts) Resolve(ctx context.Context, identity resource.Identity) (string, error) {
	user, found, err := a.store.Find(ctx, identity)
	if err != nil || found {
		return user, err
	}
	if a.policy.DefaultOwner.Subject != "" && identity == a.policy.DefaultOwner {
		user, claimed, err := a.store.ClaimBuiltin(ctx, identity)
		if err != nil || claimed {
			return user, err
		}
	}
	if !a.policy.SignUp {
		return "", ErrNoAccount
	}
	return a.create(ctx, identity)
}

// create makes a new account. Two first requests from one identity race to
// create it, and the unique index lets one through; the other finds the
// account its twin made.
func (a *Accounts) create(ctx context.Context, identity resource.Identity) (string, error) {
	user := newUserID()
	createErr := a.store.Create(ctx, user, identity)
	if createErr == nil {
		a.log.InfoContext(ctx, "account created", "user", user, "issuer", identity.Issuer)
		return user, nil
	}
	existing, found, err := a.store.Find(ctx, identity)
	if err != nil {
		return "", errors.Join(createErr, err)
	}
	if !found {
		return "", fmt.Errorf("create account: %w", createErr)
	}
	return existing, nil
}

// usernameShape is what a username may hold, the same rule the users table
// checks. A claim that does not fit is not recorded.
var usernameShape = regexp.MustCompile(`^[a-z0-9._@+-]{1,128}$`)

// UsernameOf is the name a token's account is invited by: its
// preferred_username, or its email where the provider sends no username,
// lower-cased. A token with neither, or one whose value does not fit, names
// none.
func UsernameOf(info *auth.TokenInfo) (string, bool) {
	for _, claim := range []string{"preferred_username", "email"} {
		stated, ok := resource.Stated(info, claim)
		if !ok {
			continue
		}
		name, ok := stated.(string)
		name = strings.ToLower(strings.TrimSpace(name))
		if ok && usernameShape.MatchString(name) {
			return name, true
		}
	}
	return "", false
}

// remember records the username a request's token names, once per account
// and name in this process. A failure is logged rather than refused: the
// request still acts for the right account, and only an invitation by name
// waits for the next sign-in.
func (a *Accounts) remember(ctx context.Context, user string, info *auth.TokenInfo) {
	name, ok := UsernameOf(info)
	if !ok {
		return
	}
	if seen, found := a.named.Load(user); found && seen == name {
		return
	}
	held, err := a.store.Name(ctx, user, name)
	switch {
	case err != nil:
		a.log.WarnContext(ctx, "username not recorded", "user", user, "err", err)
		return
	case !held:
		a.log.WarnContext(ctx, "username belongs to another account; this one keeps none",
			"user", user, "username", name)
	}
	a.named.Store(user, name)
}

// newUserID is a fresh identifier. It becomes part of a table name, so it is
// lower case to meet users_id_is_identifier_safe, and random rather than
// derived from the subject, which a provider may change its format for.
func newUserID() string {
	return strings.ToLower(rand.Text())
}

// allowed reports whether a token carries the required claim.
func (a *Accounts) allowed(info *auth.TokenInfo) bool {
	required := a.policy.Required
	if required.Name == "" {
		return true
	}
	stated, ok := resource.Stated(info, required.Name)
	if !ok {
		return false
	}
	switch value := stated.(type) {
	case string:
		return value == required.Value
	case []any:
		return slices.Contains(value, any(required.Value))
	default:
		return false
	}
}

// Middleware names the user a verified request acts for. It runs behind
// resource.Server.Protect, which put the token's identity in the context.
func (a *Accounts) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := auth.TokenInfoFromContext(r.Context())
		if !a.allowed(info) {
			http.Error(w, ErrNotAllowed.Error(), http.StatusForbidden)
			return
		}
		identity, err := resource.IdentityOf(info)
		if err != nil {
			a.log.ErrorContext(r.Context(), "account middleware outside Protect", "err", err)
			http.Error(w, "no identity", http.StatusInternalServerError)
			return
		}
		user, err := a.Resolve(r.Context(), identity)
		switch {
		case errors.Is(err, ErrNoAccount):
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		case err != nil:
			a.log.ErrorContext(r.Context(), "account not resolved", "err", err)
			http.Error(w, "account not resolved", http.StatusInternalServerError)
			return
		}
		a.remember(r.Context(), user, info)
		next.ServeHTTP(w, r.WithContext(owner.With(r.Context(), user)))
	})
}
