package storage

import (
	"sync"

	"api-monitor/internal/checker"
)

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
type MemoryStore struct {
	mu        sync.RWMutex
	maxPerURL int
	results   map[string][]checker.CheckResult // oldest first
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
	}
}

// SaveResults appends the batch, evicting the oldest entries per URL.
func (m *MemoryStore) SaveResults(results []checker.CheckResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range results {
		list := append(m.results[r.URL], r)
		if excess := len(list) - m.maxPerURL; excess > 0 {
			list = list[excess:]
		}
		m.results[r.URL] = list
	}
	return nil
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
