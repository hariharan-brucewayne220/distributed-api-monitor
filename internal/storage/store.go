package storage

import (
	"sort"
	"sync"
	"time"

	"api-monitor/internal/checker"
)

// MaxURLLength bounds the URLs callers may hand to a Store. The url column is
// TEXT and imposes no limit of its own, but it is indexed, and a Postgres
// btree entry cannot exceed roughly 2704 bytes; refusing longer URLs at the
// edge keeps an unusable URL from failing the INSERT for a whole batch.
const MaxURLLength = 2000

// Store is the persistence contract shared by the web server, the monitor
// CLI and the query CLI. PostgresStore implements it against a database;
// MemoryStore implements it in-process for when no DATABASE_URL is set.
type Store interface {
	// SaveResults persists one batch of check results.
	SaveResults(results []checker.CheckResult) error
	// GetRecentResults returns up to limit results for url, newest first.
	GetRecentResults(url string, limit int) ([]checker.CheckResult, error)
	// Close releases any underlying resources.
	Close() error
}

// MemoryStore keeps the most recent results per URL in memory. It is the
// fallback when persistence is disabled, so the /api/history endpoint still
// works (for the lifetime of the process).
//
// It also implements Coordinator with the same lease semantics as
// PostgresStore, but only within one process: two processes each holding a
// MemoryStore share nothing.
type MemoryStore struct {
	mu        sync.RWMutex
	maxPerURL int
	results   map[string][]checker.CheckResult // oldest first

	endpoints []*memEndpoint // registry order
	seeded    bool
	workers   map[string]*WorkerInfo
	now       func() time.Time
}

type memEndpoint struct {
	url         string
	interval    time.Duration
	nextCheckAt time.Time
	leaseOwner  string
	leaseUntil  time.Time
	leaseToken  int64
}

// DefaultMemoryCapacity is the per-URL ring size used by NewMemoryStore when
// a non-positive capacity is given.
const DefaultMemoryCapacity = 500

// NewMemoryStore creates an in-memory store retaining at most maxPerURL
// results for each URL.
func NewMemoryStore(maxPerURL int) *MemoryStore {
	if maxPerURL <= 0 {
		maxPerURL = DefaultMemoryCapacity
	}
	return &MemoryStore{
		maxPerURL: maxPerURL,
		results:   make(map[string][]checker.CheckResult),
		workers:   make(map[string]*WorkerInfo),
		now:       time.Now,
	}
}

// SetClock replaces the clock used for scheduling and leases. It is meant for
// tests that need to move time forward without sleeping.
func (m *MemoryStore) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

// SaveResults appends the batch, evicting the oldest entries per URL.
func (m *MemoryStore) SaveResults(results []checker.CheckResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range results {
		m.appendLocked(r)
	}
	return nil
}

func (m *MemoryStore) appendLocked(r checker.CheckResult) {
	list := append(m.results[r.URL], r)
	if excess := len(list) - m.maxPerURL; excess > 0 {
		list = list[excess:]
	}
	m.results[r.URL] = list
}

// GetRecentResults returns up to limit results for url, newest first, matching
// the ordering PostgresStore produces.
func (m *MemoryStore) GetRecentResults(url string, limit int) ([]checker.CheckResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := m.results[url]
	if limit <= 0 || limit > len(list) {
		limit = len(list)
	}
	out := make([]checker.CheckResult, 0, limit)
	for i := len(list) - 1; i >= len(list)-limit; i-- {
		out = append(out, list[i])
	}
	return out, nil
}

// Close is a no-op for the in-memory store.
func (m *MemoryStore) Close() error { return nil }

// Compile-time check that MemoryStore satisfies Coordinator.
var _ Coordinator = (*MemoryStore)(nil)

func (m *MemoryStore) findLocked(url string) (int, *memEndpoint) {
	for i, e := range m.endpoints {
		if e.url == url {
			return i, e
		}
	}
	return -1, nil
}

// ListEndpoints returns the registered URLs in registry order.
func (m *MemoryStore) ListEndpoints() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	urls := make([]string, 0, len(m.endpoints))
	for _, e := range m.endpoints {
		urls = append(urls, e.url)
	}
	return urls, nil
}

// AddEndpoint registers url, due immediately.
func (m *MemoryStore) AddEndpoint(url string, interval time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, e := m.findLocked(url); e != nil {
		return ErrEndpointExists
	}
	m.endpoints = append(m.endpoints, &memEndpoint{url: url, interval: interval, nextCheckAt: m.now()})
	return nil
}

// RemoveEndpoint unregisters url.
func (m *MemoryStore) RemoveEndpoint(url string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, e := m.findLocked(url)
	if e == nil {
		return ErrEndpointNotFound
	}
	m.endpoints = append(m.endpoints[:i], m.endpoints[i+1:]...)
	return nil
}

// SeedEndpoints registers urls on the first call only.
func (m *MemoryStore) SeedEndpoints(urls []string, interval time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seeded {
		return nil
	}
	m.seeded = true
	now := m.now()
	for _, u := range urls {
		if _, e := m.findLocked(u); e == nil {
			m.endpoints = append(m.endpoints, &memEndpoint{url: u, interval: interval, nextCheckAt: now})
		}
	}
	return nil
}

// ClaimDue leases up to max due, unleased endpoints, earliest due first.
func (m *MemoryStore) ClaimDue(owner string, max int, lease time.Duration) ([]Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()

	var due []*memEndpoint
	for _, e := range m.endpoints {
		if !e.nextCheckAt.After(now) && (e.leaseOwner == "" || e.leaseUntil.Before(now)) {
			due = append(due, e)
		}
	}
	// Earliest due first, stable so ties keep registry order.
	sort.SliceStable(due, func(i, j int) bool { return due[i].nextCheckAt.Before(due[j].nextCheckAt) })
	if max > 0 && len(due) > max {
		due = due[:max]
	}

	leases := make([]Lease, 0, len(due))
	for _, e := range due {
		e.leaseOwner = owner
		e.leaseUntil = now.Add(lease)
		e.leaseToken++
		leases = append(leases, Lease{URL: e.url, Token: e.leaseToken})
	}
	return leases, nil
}

// Complete records results whose lease token is still current.
func (m *MemoryStore) Complete(owner string, done []Completed) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	recorded := 0
	for _, d := range done {
		_, e := m.findLocked(d.Lease.URL)
		if e == nil || e.leaseToken != d.Lease.Token {
			continue
		}
		e.nextCheckAt = now.Add(e.interval)
		e.leaseOwner = ""
		e.leaseUntil = time.Time{}
		r := d.Result
		r.WorkerID = owner
		m.appendLocked(r)
		recorded++
	}
	return recorded, nil
}

// Heartbeat records that a worker is alive.
func (m *MemoryStore) Heartbeat(w WorkerInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if existing, ok := m.workers[w.ID]; ok {
		existing.Region = w.Region
		existing.LastHeartbeat = now
		return nil
	}
	w.LastHeartbeat = now
	if w.StartedAt.IsZero() {
		w.StartedAt = now
	}
	m.workers[w.ID] = &w
	return nil
}

// ListWorkers returns workers seen within workerRetention, by ID.
func (m *MemoryStore) ListWorkers() ([]WorkerInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := m.now()
	var out []WorkerInfo
	for _, w := range m.workers {
		since := now.Sub(w.LastHeartbeat)
		if since > workerRetention {
			continue
		}
		info := *w
		info.SinceHeartbeat = since
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// LatestResults returns the newest result per registered endpoint.
func (m *MemoryStore) LatestResults() ([]checker.CheckResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []checker.CheckResult
	for _, e := range m.endpoints {
		if list := m.results[e.url]; len(list) > 0 {
			out = append(out, list[len(list)-1])
		}
	}
	return out, nil
}
