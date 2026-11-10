package document

import (
	"errors"
	"regexp"
)

// CopyRequest is a document to copy, or move, into another library.
//
// From and To are library identifiers, already resolved from what the
// person named. The repository checks the roles again, through the row
// policies, whatever the caller checked first.
type CopyRequest struct {
	From  string
	DocID string
	To    string
	// As is the doc_id the copy takes in To. Empty keeps DocID.
	As string
	// Move removes the document from From once its copy is ready.
	Move bool
}

// Target is the doc_id the copy takes in To.
func (r CopyRequest) Target() string {
	if r.As != "" {
		return r.As
	}
	return r.DocID
}

var (
	// ErrNotReady is a document still being read, which has nothing to copy
	// until it is.
	ErrNotReady = errors.New("the document is still being ingested; copy it once it is ready")
	// ErrSameLibrary is a copy into the library the document is already in.
	ErrSameLibrary = errors.New("the document is already in that library")
	// ErrCannotMove is a move by someone who does not edit the source.
	ErrCannotMove = errors.New("moving a document needs editor in the library it leaves")
	// ErrCannotAdd is a copy into a library the person does not edit.
	ErrCannotAdd = errors.New("copying a document needs editor in the library it goes to")
	// ErrOriginalGone is an uploaded document whose stored file is missing.
	ErrOriginalGone = errors.New("the uploaded file this document was read from is gone")
	// ErrDocIDForm is an --as identifier no document could be filed under.
	ErrDocIDForm = errors.New(
		"a doc_id is lower-case letters and digits, joined by single hyphens",
	)
)

// docIDForm is what Slugify produces, numbered or not.
var docIDForm = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// ValidDocID reports whether a document could be filed under id.
func ValidDocID(id string) bool {
	return len(id) <= 200 && docIDForm.MatchString(id)
}

// Taken is a target library already filing another document under the
// copy's doc_id.
type Taken struct {
	DocID string
}

func (e Taken) Error() string {
	return e.DocID + " is taken in that library: pick another with --as"
}

// AlreadyHeld is a target library already holding the same bytes, under
// DocID, so a copy would add nothing.
type AlreadyHeld struct {
	DocID string
}

func (e AlreadyHeld) Error() string {
	return "that library already holds this document as " + e.DocID
}

// SourceHeld is a target library that already reads the same path, URL or
// upload, under DocID. Refreshing that document is the way to update it.
type SourceHeld struct {
	DocID string
}

func (e SourceHeld) Error() string {
	return "that library already reads the same source as " + e.DocID + "; refresh it there"
}
