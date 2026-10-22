package storage

import (
	"errors"
	"time"

	"api-monitor/internal/checker"
)

var (
	// ErrEndpointExists is returned by AddEndpoint for a URL already registered.
	ErrEndpointExists = errors.New("endpoint already monitored")
	// ErrEndpointNotFound is returned by RemoveEndpoint for an unknown URL.
	ErrEndpointNotFound = errors.New("endpoint not found")
)

// Lease is one worker's claim on one endpoint's due check. Token is a fencing
// token: it increases every time the endpoint is claimed, and Complete only
// accepts a result whose token is still current. A worker that stalls past its
// lease and finishes after another worker has re-claimed the endpoint has its
// result discarded instead of overwriting the newer claim's schedule.
type Lease struct {
	URL   string
	Token int64
}

// Completed pairs a lease with the check it produced.
type Completed struct {
	Lease  Lease
	Result checker.CheckResult
}

// WorkerInfo describes a worker process as last reported by its heartbeat.
type WorkerInfo struct {
	ID            string
	Region        string
	StartedAt     time.Time
	LastHeartbeat time.Time
	// SinceHeartbeat is measured on the coordinator's clock, so it does not
	// depend on the reader's clock agreeing with the worker's.
	SinceHeartbeat time.Duration
}

// Coordinator is the shared state that lets any number of worker processes
// split the checking work between them. It holds the endpoint registry, hands
// out time-limited leases on endpoints that are due, and records results only
// from the current lease holder.
//
// PostgresStore implements it across processes (row locks with SKIP LOCKED,
// lease expiry on the database clock). MemoryStore implements the same
// semantics inside one process, for running without DATABASE_URL.
type Coordinator interface {
	Store

	// ListEndpoints returns the registered URLs in the order they were added.
	ListEndpoints() ([]string, error)
	// AddEndpoint registers url to be checked every interval, starting now.
	AddEndpoint(url string, interval time.Duration) error
	// RemoveEndpoint unregisters url. Results already stored are kept.
	RemoveEndpoint(url string) error
	// SeedEndpoints registers urls the first time it is called against a
	// given coordinator, and is a no-op on every later call, so a registry
	// the user has edited is never re-seeded on restart.
	SeedEndpoints(urls []string, interval time.Duration) error

	// ClaimDue leases up to max endpoints whose next check is due and that no
	// live lease covers, for the given duration.
	ClaimDue(owner string, max int, lease time.Duration) ([]Lease, error)
	// Complete records each result whose lease is still current and schedules
	// that endpoint's next check. It returns how many results were recorded.
	Complete(owner string, done []Completed) (int, error)

	// Heartbeat records that a worker is alive.
	Heartbeat(w WorkerInfo) error
	// ListWorkers returns workers that have sent a heartbeat recently.
	ListWorkers() ([]WorkerInfo, error)

	// LatestResults returns the most recent result for each registered
	// endpoint, in registry order. Endpoints never checked are omitted.
	LatestResults() ([]checker.CheckResult, error)
}

// workerRetention is how long a worker that stopped heartbeating stays listed.
const workerRetention = time.Hour
