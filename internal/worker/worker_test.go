package worker

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"api-monitor/internal/checker"
	"api-monitor/internal/storage"
)

// hitCounter is a target server that counts requests per path.
type hitCounter struct {
	mu   sync.Mutex
	hits map[string]int
	srv  *httptest.Server
}

func newHitCounter(t *testing.T) *hitCounter {
	t.Helper()
	h := &hitCounter{hits: map[string]int{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.hits[r.URL.Path]++
		h.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hitCounter) urls(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s/ep/%d", h.srv.URL, i)
	}
	return out
}

func (h *hitCounter) count(url string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[url[len(h.srv.URL):]]
}

func newWorker(id string, coord storage.Coordinator, batch int) *Worker {
	return &Worker{
		ID:            id,
		Coord:         coord,
		Checker:       checker.NewHTTPChecker(2 * time.Second),
		BatchSize:     batch,
		LeaseDuration: 10 * time.Second,
		PollInterval:  20 * time.Millisecond,
	}
}

func TestWorker_RunOnceRecordsWithWorkerID(t *testing.T) {
	target := newHitCounter(t)
	coord := storage.NewMemoryStore(100)
	coord.SeedEndpoints(target.urls(3), time.Minute)

	w := newWorker("w1", coord, 10)
	claimed, recorded, err := w.RunOnce()
	if err != nil || claimed != 3 || recorded != 3 {
		t.Fatalf("RunOnce = %d, %d, %v; want 3, 3, nil", claimed, recorded, err)
	}
	latest, _ := coord.LatestResults()
	for _, r := range latest {
		if r.WorkerID != "w1" || !r.IsHealthy {
			t.Errorf("result %+v", r)
		}
	}
	if workers, _ := coord.ListWorkers(); len(workers) != 1 || workers[0].ID != "w1" {
		t.Errorf("heartbeat not recorded: %+v", workers)
	}
	// Nothing is due again until the interval passes.
	if claimed, _, _ := w.RunOnce(); claimed != 0 {
		t.Errorf("second round claimed %d", claimed)
	}
}

func TestWorker_SeveralWorkersCheckEachEndpointOnce(t *testing.T) {
	target := newHitCounter(t)
	urls := target.urls(20)
	coord := storage.NewMemoryStore(100)
	coord.SeedEndpoints(urls, time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(w *Worker) {
			defer wg.Done()
			for {
				claimed, _, err := w.RunOnce()
				if err != nil {
					t.Error(err)
					return
				}
				if claimed == 0 {
					return
				}
			}
		}(newWorker(fmt.Sprintf("w%d", i), coord, 3))
	}
	wg.Wait()

	for _, u := range urls {
		if n := target.count(u); n != 1 {
			t.Errorf("%s hit %d times, want 1", u, n)
		}
	}
}

// TestWorker_PostgresCluster runs three workers, each with its own connection
// pool, against one database and a set of real HTTP targets for a few
// seconds, then stops one. It checks the properties that make this a cluster
// rather than three monitors: the work is spread across workers, no endpoint
// is checked twice in one interval, and coverage continues when a worker
// leaves.
func TestWorker_PostgresCluster(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live Postgres test")
	}
	dsn += " search_path=" + createSchema(t, dsn)

	const interval = 300 * time.Millisecond
	target := newHitCounter(t)
	urls := target.urls(12)

	var peers []*storage.PostgresStore
	for i := 0; i < 3; i++ {
		p, err := storage.NewPostgresStore(dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Close() })
		peers = append(peers, p)
	}
	if err := peers[0].SeedEndpoints(urls, interval); err != nil {
		t.Fatal(err)
	}

	ctxs := make([]context.CancelFunc, 3)
	var wg sync.WaitGroup
	for i, p := range peers {
		ctx, cancel := context.WithCancel(context.Background())
		ctxs[i] = cancel
		// A batch smaller than the endpoint count forces the work to spread.
		w := newWorker(fmt.Sprintf("w%d", i), p, 4)
		wg.Add(1)
		go func() { defer wg.Done(); w.Run(ctx) }()
	}

	time.Sleep(3 * time.Second)
	ctxs[0]() // w0 leaves the cluster
	stoppedAt := time.Now()
	time.Sleep(2 * time.Second)
	ctxs[1]()
	ctxs[2]()
	wg.Wait()

	byWorker := map[string]int{}
	for _, u := range urls {
		rows, err := peers[0].GetRecentResults(u, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if got := target.count(u); got != len(rows) {
			t.Errorf("%s: target saw %d requests but %d results were recorded", u, got, len(rows))
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].CheckedAt.Before(rows[j].CheckedAt) })
		for i := 1; i < len(rows); i++ {
			if gap := rows[i].CheckedAt.Sub(rows[i-1].CheckedAt); gap < interval/2 {
				t.Errorf("%s checked twice %v apart (by %s and %s), interval is %v",
					u, gap, rows[i-1].WorkerID, rows[i].WorkerID, interval)
			}
		}
		if last := rows[len(rows)-1]; last.CheckedAt.Before(stoppedAt.Add(time.Second)) {
			t.Errorf("%s not checked after w0 left (last %v by %s)", u, last.CheckedAt, last.WorkerID)
		}
		for _, r := range rows {
			byWorker[r.WorkerID]++
		}
	}
	t.Logf("results per worker: %v", byWorker)
	for _, id := range []string{"w0", "w1", "w2"} {
		if byWorker[id] == 0 {
			t.Errorf("worker %s recorded nothing; work was not distributed", id)
		}
	}
}

func createSchema(t *testing.T, dsn string) string {
	t.Helper()
	schema := fmt.Sprintf("api_monitor_worker_test_%d", time.Now().UnixNano())
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			t.Error(err)
			return
		}
		defer db.Close()
		if _, err := db.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	return schema
}
