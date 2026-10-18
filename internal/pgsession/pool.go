package pgsession

import (
	"database/sql"
	"time"
)

// Pool sizes for the two long-running processes. database/sql opens a new
// connection whenever none is free, without limit, so a burst of requests
// would open one each until Postgres refused everybody, the worker included.
// Bounded, a burst waits for a connection instead.
const (
	// ServerConnections serves requests, each holding a connection for one
	// short transaction at a time.
	ServerConnections = 20
	// WorkerConnections covers one job's writes, its lease renewal and its
	// progress, with room for the claim sweep.
	WorkerConnections = 4
)

// idleTimeout closes a connection nothing has used for a while, so a quiet
// process does not hold its peak.
const idleTimeout = 5 * time.Minute

// Bound caps db at open connections.
func Bound(db *sql.DB, open int) {
	db.SetMaxOpenConns(open)
	db.SetMaxIdleConns(open)
	db.SetConnMaxIdleTime(idleTimeout)
}
