package storage

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// These tests open several PostgresStores on one schema. Each has its own
// connection pool, so they stand in for separate worker processes: nothing is
// shared between them except the database. Like the other Postgres tests they
// are skipped unless TEST_DATABASE_URL is set.

// openPeers opens n stores on one fresh schema, concurrently, which also
// exercises the advisory lock around schema setup: without it, simultaneous
// CREATE TABLE IF NOT EXISTS from several processes fails on pg_type.
func openPeers(t *testing.T, n int) []*PostgresStore {
	t.Helper()
	dsn := testDSN(t)
	schema := createTestSchema(t, dsn)

	stores := make([]*PostgresStore, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stores[i], errs[i] = NewPostgresStore(dsn + " search_path=" + schema)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("peer %d: NewPostgresStore: %v", i, err)
		}
		s := stores[i]
		t.Cleanup(func() { s.Close() })
	}
	return stores
}

func TestPostgresCoordinator_ConcurrentSetupAndSeedOnce(t *testing.T) {
	peers := openPeers(t, 8)

	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func(p *PostgresStore) {
			defer wg.Done()
			if err := p.SeedEndpoints([]string{"https://a", "https://b"}, time.Minute); err != nil {
				t.Errorf("seed: %v", err)
			}
		}(p)
	}
	wg.Wait()

	if err := peers[0].RemoveEndpoint("https://a"); err != nil {
		t.Fatal(err)
	}
	// A later restart must not re-seed what the user removed.
	if err := peers[1].SeedEndpoints([]string{"https://a", "https://b"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	urls, err := peers[2].ListEndpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[0] != "https://b" {
		t.Errorf("endpoints = %v, want [https://b]", urls)
	}
}

func TestPostgresCoordinator_ConcurrentClaimsNeverOverlap(t *testing.T) {
	const workers, endpoints = 6, 60
	peers := openPeers(t, workers)

	urls := make([]string, endpoints)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://example.test/%d", i)
	}
	if err := peers[0].SeedEndpoints(urls, time.Minute); err != nil {
		t.Fatal(err)
	}

	// Every worker claims in small batches, as fast as it can, until nothing
	// is left. With SKIP LOCKED each endpoint must go to exactly one worker.
	claimedBy := make(map[string][]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, p := range peers {
		wg.Add(1)
		go func(owner string, p *PostgresStore) {
			defer wg.Done()
			for {
				leases, err := p.ClaimDue(owner, 3, time.Minute)
				if err != nil {
					t.Errorf("%s claim: %v", owner, err)
					return
				}
				if len(leases) == 0 {
					return
				}
				mu.Lock()
				for _, l := range leases {
					claimedBy[l.URL] = append(claimedBy[l.URL], owner)
				}
				mu.Unlock()
			}
		}(fmt.Sprintf("w%d", i), p)
	}
	wg.Wait()

	if len(claimedBy) != endpoints {
		t.Fatalf("%d endpoints claimed, want %d", len(claimedBy), endpoints)
	}
	perWorker := map[string]int{}
	for url, owners := range claimedBy {
		if len(owners) != 1 {
			t.Errorf("%s claimed by %v, want exactly one worker", url, owners)
		}
		perWorker[owners[0]]++
	}
	t.Logf("claims per worker: %v", perWorker)
}

func TestPostgresCoordinator_ExpiredLeaseIsTakenOverAndFenced(t *testing.T) {
	peers := openPeers(t, 2)
	crashed, survivor := peers[0], peers[1]

	if err := crashed.SeedEndpoints([]string{"https://a"}, time.Minute); err != nil {
		t.Fatal(err)
	}

	stale, err := crashed.ClaimDue("crashed", 10, 300*time.Millisecond)
	if err != nil || len(stale) != 1 {
		t.Fatalf("claim = %+v, %v", stale, err)
	}
	if got, _ := survivor.ClaimDue("survivor", 10, time.Minute); len(got) != 0 {
		t.Fatalf("survivor claimed %+v while the lease is live", got)
	}

	time.Sleep(400 * time.Millisecond)
	fresh, err := survivor.ClaimDue("survivor", 10, time.Minute)
	if err != nil || len(fresh) != 1 {
		t.Fatalf("after expiry survivor claimed %+v, %v; want the endpoint", fresh, err)
	}

	// The "crashed" worker was only slow and now reports back. Its token is
	// stale, so its result is dropped and the schedule stays with survivor.
	if n := complete(t, crashed, "crashed", stale); n != 0 {
		t.Errorf("stale lease recorded %d results", n)
	}
	if n := complete(t, survivor, "survivor", fresh); n != 1 {
		t.Errorf("current lease recorded %d results, want 1", n)
	}

	latest, err := crashed.LatestResults()
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].WorkerID != "survivor" {
		t.Errorf("latest = %+v, want one result from survivor", latest)
	}
	if again, _ := crashed.ClaimDue("crashed", 10, time.Minute); len(again) != 0 {
		t.Errorf("endpoint due again right after completion: %+v", again)
	}
}

func TestPostgresCoordinator_RegistryWorkersAndRemovedEndpoint(t *testing.T) {
	peers := openPeers(t, 2)
	a, b := peers[0], peers[1]

	if err := a.AddEndpoint("https://x", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := b.AddEndpoint("https://x", time.Minute); !errors.Is(err, ErrEndpointExists) {
		t.Errorf("duplicate add from another process: err = %v", err)
	}
	if err := b.RemoveEndpoint("https://nope"); !errors.Is(err, ErrEndpointNotFound) {
		t.Errorf("unknown remove: err = %v", err)
	}

	leases, _ := a.ClaimDue("w", 10, time.Minute)
	if err := b.RemoveEndpoint("https://x"); err != nil {
		t.Fatal(err)
	}
	if n := complete(t, a, "w", leases); n != 0 {
		t.Errorf("recorded %d results for an endpoint removed mid-check", n)
	}

	if err := a.Heartbeat(WorkerInfo{ID: "w-a", Region: "us-east"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Heartbeat(WorkerInfo{ID: "w-b", Region: "eu-west"}); err != nil {
		t.Fatal(err)
	}
	workers, err := a.ListWorkers()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 || workers[1].Region != "eu-west" || workers[0].SinceHeartbeat > 5*time.Second {
		t.Errorf("workers = %+v", workers)
	}
}
