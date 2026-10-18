package password

import "github.com/alexedwards/argon2id"

// ConcurrentChecks is the bound on password checks, for the test that holds it.
const ConcurrentChecks = concurrentChecks

// Params are the costs a new hash is made with.
func Params() *argon2id.Params { return params }
