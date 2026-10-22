package storage

import (
	"errors"
	"sync"
	"testing"
	"time"

	"api-monitor/internal/checker"
)

// fakeClock is a manually advanced clock for MemoryStore.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClockedMemory(t *testing.T, urls ...string) (*MemoryStore, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	m := NewMemoryStore(100)
	m.SetClock(clock.Now)
	if err := m.SeedEndpoints(urls, time.Minute); err != nil {
		t.Fatal(err)
	}
	return m, clock
}

func complete(t *testing.T, c Coordinator, owner string, leases []Lease) int {
	t.Helper()
	done := make([]Completed, len(leases))
	for i, l := range leases {
		done[i] = Completed{Lease: l, Result: checker.CheckResult{URL: l.URL, StatusCode: 200, IsHealthy: true, CheckedAt: time.Now()}}
	}
	n, err := c.Complete(owner, done)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMemoryCoordinator_ClaimIsExclusiveUntilLeaseExpires(t *testing.T) {
	m, clock := newClockedMemory(t, "https://a", "https://b", "https://c")

	first, _ := m.ClaimDue("w1", 2, 30*time.Second)
	if len(first) != 2 || first[0].URL != "https://a" || first[1].URL != "https://b" {
		t.Fatalf("w1 claimed %+v, want a and b", first)
	}
	second, _ := m.ClaimDue("w2", 10, 30*time.Second)
	if len(second) != 1 || second[0].URL != "https://c" {
		t.Fatalf("w2 claimed %+v, want only c", second)
	}
	if more, _ := m.ClaimDue("w3", 10, 30*time.Second); len(more) != 0 {
		t.Fatalf("w3 claimed %+v while every lease is live", more)
	}

	// w1 dies without completing. Once its leases expire, w3 takes them over.
	clock.Advance(31 * time.Second)
	taken, _ := m.ClaimDue("w3", 10, 30*time.Second)
	if len(taken) != 3 {
		t.Fatalf("after expiry w3 claimed %d endpoints, want 3 (a, b and c)", len(taken))
	}
}

func TestMemoryCoordinator_StaleLeaseCannotRecord(t *testing.T) {
	m, clock := newClockedMemory(t, "https://a")

	stale, _ := m.ClaimDue("slow", 1, 10*time.Second)
	clock.Advance(11 * time.Second)
	fresh, _ := m.ClaimDue("fast", 1, 10*time.Second)
	if len(fresh) != 1 || fresh[0].Token <= stale[0].Token {
		t.Fatalf("re-claim should issue a newer token: stale %+v fresh %+v", stale, fresh)
	}

	if n := complete(t, m, "slow", stale); n != 0 {
		t.Errorf("stale lease recorded %d results, want 0", n)
	}
	if n := complete(t, m, "fast", fresh); n != 1 {
		t.Errorf("current lease recorded %d results, want 1", n)
	}

	rows, _ := m.GetRecentResults("https://a", 0)
	if len(rows) != 1 || rows[0].WorkerID != "fast" {
		t.Errorf("stored %+v, want one row from fast", rows)
	}
}

func TestMemoryCoordinator_CompleteSchedulesNextCheck(t *testing.T) {
	m, clock := newClockedMemory(t, "https://a")

	leases, _ := m.ClaimDue("w", 1, 10*time.Second)
	complete(t, m, "w", leases)

	if again, _ := m.ClaimDue("w", 1, 10*time.Second); len(again) != 0 {
		t.Fatalf("endpoint due again immediately after completion")
	}
	clock.Advance(time.Minute)
	if again, _ := m.ClaimDue("w", 1, 10*time.Second); len(again) != 1 {
		t.Fatalf("endpoint not due one interval after completion")
	}
}

func TestMemoryCoordinator_RemovedEndpointDiscardsInFlightResult(t *testing.T) {
	m, _ := newClockedMemory(t, "https://a")
	leases, _ := m.ClaimDue("w", 1, 10*time.Second)
	if err := m.RemoveEndpoint("https://a"); err != nil {
		t.Fatal(err)
	}
	if n := complete(t, m, "w", leases); n != 0 {
		t.Errorf("recorded %d results for a removed endpoint", n)
	}
}

func TestMemoryCoordinator_RegistryAndSeedOnce(t *testing.T) {
	m, _ := newClockedMemory(t, "https://a", "https://b")

	if err := m.AddEndpoint("https://a", time.Minute); !errors.Is(err, ErrEndpointExists) {
		t.Errorf("duplicate add err = %v", err)
	}
	if err := m.RemoveEndpoint("https://nope"); !errors.Is(err, ErrEndpointNotFound) {
		t.Errorf("unknown remove err = %v", err)
	}
	if err := m.RemoveEndpoint("https://a"); err != nil {
		t.Fatal(err)
	}
	// A restart re-seeding must not resurrect what the user removed.
	if err := m.SeedEndpoints([]string{"https://a", "https://b"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	urls, _ := m.ListEndpoints()
	if len(urls) != 1 || urls[0] != "https://b" {
		t.Errorf("endpoints = %v, want [https://b]", urls)
	}
}

func TestMemoryCoordinator_LatestResultsAndWorkers(t *testing.T) {
	m, clock := newClockedMemory(t, "https://a", "https://b")

	leases, _ := m.ClaimDue("w1", 1, 10*time.Second) // only a
	complete(t, m, "w1", leases)

	latest, _ := m.LatestResults()
	if len(latest) != 1 || latest[0].URL != "https://a" || latest[0].WorkerID != "w1" {
		t.Errorf("latest = %+v, want a checked by w1 (b never checked)", latest)
	}

	m.Heartbeat(WorkerInfo{ID: "w2", Region: "eu"})
	clock.Advance(5 * time.Second)
	m.Heartbeat(WorkerInfo{ID: "w1", Region: "us"})
	workers, _ := m.ListWorkers()
	if len(workers) != 2 || workers[0].ID != "w1" || workers[1].SinceHeartbeat != 5*time.Second {
		t.Errorf("workers = %+v", workers)
	}

	clock.Advance(2 * time.Hour)
	if workers, _ := m.ListWorkers(); len(workers) != 0 {
		t.Errorf("workers silent for 2h still listed: %+v", workers)
	}
}
