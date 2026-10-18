// Package owner carries the user a request acts for.
//
// Row-level security decides what a statement sees from app.user_id, so every
// read and write needs a user before it reaches the database. A request that
// names none is refused here rather than defaulted: defaulting would hand
// whoever asked the built-in user's library, which is the one failure
// multi-user isolation exists to prevent.
package owner

import (
	"context"
	"errors"
	"net/http"
)

// Builtin is the user a single-library deployment serves.
//
// Migration 6 created the row, and switching to a mode with accounts hands
// that library to the first account created, so an install that never had
// accounts survives the move.
const Builtin = "default"

// ErrNoOwner reports a request that reached a handler without a user.
//
// A handler mounted outside the middleware is the way that happens, so the
// message names the cause rather than asking the caller to supply something
// no caller sends.
var ErrNoOwner = errors.New(
	"no owner: the request reached a handler without passing the middleware that names a user")

// contextKey is private, so nothing outside this package can plant an owner
// without going through With.
type contextKey struct{}

// With returns a context naming the user a request acts for.
func With(ctx context.Context, user string) context.Context {
	return context.WithValue(ctx, contextKey{}, user)
}

// From is the user a request acts for.
func From(ctx context.Context) (string, error) {
	user, ok := ctx.Value(contextKey{}).(string)
	if !ok || user == "" {
		return "", ErrNoOwner
	}
	return user, nil
}

// Middleware names the user every request through it acts for.
//
// Phase 04 serves one library, so the user is the same for every request and
// the bearer token authenticates without identifying. An identity provider
// replaces the argument here and nothing downstream changes, because a
// handler asks From rather than reading a token.
func Middleware(user string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(With(r.Context(), user)))
	})
}
