// Package sharing names what managing a shared library deals in: who has
// which role, the record of changes, and the refusals.
//
// The rules themselves live in the database, in the definer functions
// migration 23 made, so every door that manages libraries enforces the same
// ones. This package is what those doors and the store agree on.
package sharing

import (
	"errors"
	"time"
)

// Roles a user can hold on a library, least first.
const (
	Reader = "reader"
	Editor = "editor"
	Admin  = "admin"
)

// Library is one library a user reads, named the way they name it.
type Library struct {
	Name  string
	Title string
	Role  string
}

// Member is one person with a role on a library. Username is empty for an
// account that has not signed in since usernames were recorded. Invited is
// someone asked to join with Role who has not yet accepted.
type Member struct {
	Username string
	Role     string
	Invited  bool
}

// Invitation is a library someone is asked to join. Accepting it gives
// Role; until then the library reaches none of their searches.
type Invitation struct {
	At        time.Time
	Library   string
	Title     string
	Role      string
	InvitedBy string
}

// Event is one change to who may use a library.
type Event struct {
	At      time.Time
	Actor   string
	Action  string
	Subject string
	Role    string
}

var (
	// ErrNoLibrary is a library the caller cannot do this to: one they do
	// not hold the role for, or one that does not exist. Both answer alike.
	ErrNoLibrary = errors.New("no library by that name that you can change: " +
		"check the name, and that you are its admin")
	// ErrNoAccount is a username no account holds. Someone who has never
	// signed in has no username yet.
	ErrNoAccount = errors.New(
		"no account by that username: ask them to sign in once, then try again",
	)
	// ErrLastAdmin is a change that would leave a library without an admin.
	ErrLastAdmin = errors.New(
		"that would leave the library without an admin: make someone else admin first",
	)
	// ErrNoInvitation is a library the caller is not invited to, whether or
	// not it exists.
	ErrNoInvitation = errors.New("no invitation to a library by that name")
	// ErrTooMany is a library past the most one account may create.
	ErrTooMany = errors.New("you have created as many libraries as one account may")
	// ErrNameTaken is a slug another library already has.
	ErrNameTaken = errors.New("a library by that name already exists")
	// ErrInvalid is a slug, name or role the database refused the shape of.
	ErrInvalid = errors.New("a library name is lower-case letters, digits and hyphens, " +
		"other than 'personal', and a role is reader, editor or admin")
)
