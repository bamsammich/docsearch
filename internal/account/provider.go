package account

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/bamsammich/docsearch/internal/resource"
)

// Provider is what an operator links to the groups their provider puts in
// its tokens. The zero value links nothing.
type Provider struct {
	// Userinfo reads the groups claim of a token that does not carry it
	// itself. Nil reads the token alone.
	Userinfo Userinfo
	// Filter limits which provider groups AutoCreate makes a group for. Nil
	// matches every one.
	Filter *regexp.Regexp
	// GroupsClaim names the claim that lists the provider groups a person
	// is in. Empty turns the provider link off.
	GroupsClaim string
	// AdminGroup is the provider group whose members are docsearch
	// administrators. Empty leaves administrators to docsearch.
	AdminGroup string
	// AutoCreate makes a linked group for each provider group a token
	// carries that Filter matches and no group follows yet.
	AutoCreate bool
}

// Userinfo reads what a provider states about the holder of a token.
type Userinfo interface {
	Claims(ctx context.Context, token, subject string) (map[string]any, error)
}

// ProviderSync is what one token says about a person's place in docsearch.
type ProviderSync struct {
	// Carried are the provider groups the token carries.
	Carried []string
	// Create are the carried groups to make a linked group for.
	Create []string
	// SetAdmin is whether the provider decides Admin at all.
	SetAdmin bool
	// Admin is whether the person is an administrator.
	Admin bool
}

// The account middleware reads each token's groups once. A token keeps
// being sent until it expires, and the digests of those already read are
// kept for at most syncedFor, so a token outliving that is read again. The
// bound keeps a caller sending many distinct tokens from growing the memory.
const (
	syncedFor  = time.Hour
	syncedKept = 10000
)

// ErrGroupsUnread is a token whose groups claim could not be read, so the
// request cannot be given the access those groups decide.
var ErrGroupsUnread = errors.New("the provider's groups for this token could not be read")

// syncer applies a Provider to each new token.
type syncer struct {
	synced   *expirable.LRU[[sha256.Size]byte, bool]
	provider Provider
}

func newSyncer(provider Provider) *syncer {
	return &syncer{
		provider: provider,
		synced:   expirable.NewLRU[[sha256.Size]byte, bool](syncedKept, nil, syncedFor),
	}
}

// on reports whether the provider link is on.
func (s *syncer) on() bool { return s.provider.GroupsClaim != "" }

// sync applies a token's groups to user, once per token. A failure leaves
// the token unmarked, so the next request tries again.
func (s *syncer) sync(ctx context.Context, store Store, user string, r *http.Request) error {
	token := bearer(r)
	digest := sha256.Sum256([]byte(token))
	if _, done := s.synced.Get(digest); done {
		return nil
	}
	info := auth.TokenInfoFromContext(r.Context())
	carried, err := s.groups(ctx, info, token)
	if err != nil {
		return err
	}
	if err := store.Sync(ctx, user, s.plan(carried)); err != nil {
		return fmt.Errorf("apply the provider's groups: %w", err)
	}
	s.synced.Add(digest, true)
	return nil
}

// groups are the provider groups a token carries: from the token itself,
// else from userinfo. A claim the provider leaves out names no group, as
// providers omit an empty groups claim.
func (s *syncer) groups(ctx context.Context, info *auth.TokenInfo, token string) ([]string, error) {
	if stated, ok := resource.Stated(info, s.provider.GroupsClaim); ok {
		return GroupNames(stated), nil
	}
	if s.provider.Userinfo == nil {
		return nil, nil
	}
	identity, err := resource.IdentityOf(info)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGroupsUnread, err)
	}
	claims, err := s.provider.Userinfo.Claims(ctx, token, identity.Subject)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGroupsUnread, err)
	}
	return GroupNames(claims[s.provider.GroupsClaim]), nil
}

// plan is what the carried groups change.
func (s *syncer) plan(carried []string) ProviderSync {
	out := ProviderSync{Carried: carried}
	if s.provider.AutoCreate {
		for _, name := range carried {
			if s.provider.Filter == nil || s.provider.Filter.MatchString(name) {
				out.Create = append(out.Create, name)
			}
		}
	}
	if s.provider.AdminGroup != "" {
		out.SetAdmin = true
		out.Admin = slices.Contains(carried, s.provider.AdminGroup)
	}
	return out
}

// GroupNames are the group names a claim's value holds, sorted and once
// each. Providers spell a groups claim three ways: a list of names, one
// name as a string, or an object keyed by name, which is how some state
// roles with details beside each.
func GroupNames(value any) []string {
	var names []string
	switch v := value.(type) {
	case string:
		names = []string{v}
	case []any:
		for _, item := range v {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
	case []string:
		names = append(names, v...)
	case map[string]any:
		for name := range v {
			names = append(names, name)
		}
	}
	names = slices.DeleteFunc(names, func(name string) bool { return name == "" })
	slices.Sort(names)
	return slices.Compact(names)
}

// bearer is the token a request carries, which Protect has already read.
func bearer(r *http.Request) string {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
