package owner

import "fmt"

// Mode is how a deployment decides which user a request acts for.
//
// Modes are what the phase 05 plan lists, and two of them exist. `none` and
// `token` both answer with the built-in user, so neither needs an account; a
// deployment with one library should not be made to run an identity provider
// to use its own documents.
type Mode int

// The zero value is deliberately not a mode. An unset mode must not resolve
// to the one that asks for no credential, so a Mode nobody parsed is
// refused rather than read as None.
const (
	None Mode = iota + 1
	Token
	Password
	OIDC
)

// names are the spellings an operator writes and that String reports back.
var names = map[Mode]string{
	None:     "none",
	Token:    "token",
	Password: "password",
	OIDC:     "oidc",
}

func (m Mode) String() string {
	if name, ok := names[m]; ok {
		return name
	}
	return fmt.Sprintf("Mode(%d)", int(m))
}

// Spellings are every mode an operator may write, for a flag's own help.
func Spellings() []string {
	return []string{None.String(), Token.String(), Password.String(), OIDC.String()}
}

// ParseMode reads the spelling an operator wrote.
//
// A mode the plan names but no step has built yet is refused by name rather
// than as an unknown word, so an operator who configured it ahead of time
// learns which it is.
func ParseMode(spelling string) (Mode, error) {
	for mode, name := range names {
		if spelling != name {
			continue
		}
		if mode == Password || mode == OIDC {
			return 0, fmt.Errorf("auth mode %q is planned but not built yet", name)
		}
		return mode, nil
	}
	return 0, fmt.Errorf("unknown auth mode %q: one of %v", spelling, Spellings())
}
