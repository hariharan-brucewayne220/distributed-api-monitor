package checker

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func newServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestCheck_Healthy200(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	before := time.Now()
	res := NewHTTPChecker(2 * time.Second).Check(srv.URL)

	if res.URL != srv.URL {
		t.Errorf("URL = %q, want %q", res.URL, srv.URL)
	}
	if !res.IsHealthy {
		t.Errorf("IsHealthy = false, want true")
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", res.StatusCode)
	}
	if res.Error != "" {
		t.Errorf("Error = %q, want empty", res.Error)
	}
	if res.ResponseTime <= 0 {
		t.Errorf("ResponseTime = %v, want > 0", res.ResponseTime)
	}
	if res.CheckedAt.Before(before) {
		t.Errorf("CheckedAt %v is before test start %v", res.CheckedAt, before)
	}
}

func TestCheck_Unhealthy500(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	res := NewHTTPChecker(2 * time.Second).Check(srv.URL)

	if res.IsHealthy {
		t.Errorf("IsHealthy = true, want false for a 500")
	}
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", res.StatusCode)
	}
	if res.Error != "" {
		t.Errorf("Error = %q, want empty (a 500 is a completed request, not a transport error)", res.Error)
	}
}

func TestCheck_Timeout(t *testing.T) {
	release := make(chan struct{})
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })

	timeout := 100 * time.Millisecond
	res := NewHTTPChecker(timeout).Check(srv.URL)

	if res.IsHealthy {
		t.Errorf("IsHealthy = true, want false on timeout")
	}
	if res.StatusCode != 0 {
		t.Errorf("StatusCode = %d, want 0 (no response received)", res.StatusCode)
	}
	if res.Error == "" {
		t.Errorf("Error is empty, want a timeout error")
	}
	if res.ResponseTime < timeout {
		t.Errorf("ResponseTime = %v, want >= timeout %v", res.ResponseTime, timeout)
	}
}

func TestCheckMultiple_ConcurrentAndOrdered(t *testing.T) {
	const n = 8
	const delay = 150 * time.Millisecond

	// Every handler blocks until all n requests have arrived, so the batch can
	// only finish if the checks really run concurrently.
	var arrived sync.WaitGroup
	arrived.Add(n)
	srvs := make([]*httptest.Server, n)
	urls := make([]string, n)
	for i := 0; i < n; i++ {
		i := i
		srvs[i] = newServer(t, func(w http.ResponseWriter, r *http.Request) {
			arrived.Done()
			arrived.Wait()
			time.Sleep(delay)
			if i%2 == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		})
		urls[i] = srvs[i].URL
	}

	start := time.Now()
	results := NewHTTPChecker(5 * time.Second).CheckMultiple(urls)
	elapsed := time.Since(start)

	if len(results) != n {
		t.Fatalf("got %d results, want %d", len(results), n)
	}
	// Sequential execution would take at least n*delay.
	if elapsed > time.Duration(n)*delay {
		t.Errorf("CheckMultiple took %v, expected concurrent execution well under %v", elapsed, time.Duration(n)*delay)
	}
	for i, res := range results {
		if res.URL != urls[i] {
			t.Errorf("results[%d].URL = %q, want %q (order must match input)", i, res.URL, urls[i])
		}
		wantHealthy := i%2 == 0
		if res.IsHealthy != wantHealthy {
			t.Errorf("results[%d].IsHealthy = %v, want %v", i, res.IsHealthy, wantHealthy)
		}
	}
}

func TestCheckMultiple_Empty(t *testing.T) {
	results := NewHTTPChecker(time.Second).CheckMultiple(nil)
	if len(results) != 0 {
		t.Errorf("got %d results for no URLs", len(results))
	}
}
