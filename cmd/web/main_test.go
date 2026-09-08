package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-monitor/internal/checker"
	"api-monitor/internal/config"
	"api-monitor/internal/storage"
)

// newTestServer builds a WebServer over two local endpoints (one healthy, one
// returning 500) and an in-memory store.
func newTestServer(t *testing.T) (*WebServer, *storage.MemoryStore, string, string) {
	t.Helper()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ok.Close)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)

	cfg := &config.Config{RequestTimeout: 2 * time.Second, AIEnabled: false}
	store := storage.NewMemoryStore(100)
	ws := NewWebServer(cfg, checker.NewHTTPChecker(cfg.RequestTimeout), store, nil, []string{ok.URL, bad.URL})
	return ws, store, ok.URL, bad.URL
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestStatus_ChecksAndPersists(t *testing.T) {
	ws, store, okURL, badURL := newTestServer(t)
	h := ws.Handler()

	rec := get(t, h, "/api/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	var statuses []EndpointStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &statuses); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if len(statuses) != 2 {
		t.Fatalf("got %d statuses, want 2", len(statuses))
	}
	if statuses[0].URL != okURL || !statuses[0].IsHealthy || statuses[0].StatusCode != 200 {
		t.Errorf("statuses[0] = %+v", statuses[0])
	}
	if statuses[1].URL != badURL || statuses[1].IsHealthy || statuses[1].StatusCode != 500 {
		t.Errorf("statuses[1] = %+v", statuses[1])
	}

	// Each /api/status call writes one batch to the store.
	get(t, h, "/api/status")
	for _, u := range []string{okURL, badURL} {
		rows, err := store.GetRecentResults(u, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Errorf("store holds %d rows for %s, want 2", len(rows), u)
		}
	}
}

func TestHistory_ReturnsStoredResultsNewestFirst(t *testing.T) {
	ws, store, okURL, _ := newTestServer(t)
	h := ws.Handler()

	// Seed three rows directly so ordering is deterministic.
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	var seed []checker.CheckResult
	for i := 0; i < 3; i++ {
		seed = append(seed, checker.CheckResult{
			URL:          okURL,
			StatusCode:   200,
			IsHealthy:    true,
			ResponseTime: time.Duration(i+1) * 10 * time.Millisecond,
			CheckedAt:    base.Add(time.Duration(i) * time.Minute),
		})
	}
	if err := store.SaveResults(seed); err != nil {
		t.Fatal(err)
	}

	rec := get(t, h, "/api/history?endpoint="+okURL+"&limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp HistoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if resp.Endpoint != okURL {
		t.Errorf("Endpoint = %q, want %q", resp.Endpoint, okURL)
	}
	if resp.Count != 2 || len(resp.Results) != 2 {
		t.Fatalf("Count = %d, len = %d, want 2", resp.Count, len(resp.Results))
	}
	if !resp.Results[0].LastChecked.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("first result should be newest, got %v", resp.Results[0].LastChecked)
	}
	if resp.Results[1].ResponseTime != 20*time.Millisecond {
		t.Errorf("second result ResponseTime = %v, want 20ms", resp.Results[1].ResponseTime)
	}
}

func TestHistory_DefaultLimitAndUnknownEndpoint(t *testing.T) {
	ws, _, _, _ := newTestServer(t)
	rec := get(t, ws.Handler(), "/api/history?endpoint=https://never.checked")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	var resp HistoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 0 || resp.Results == nil {
		t.Errorf("want empty (non-null) results, got %s", rec.Body.String())
	}
}

func TestHistory_Validation(t *testing.T) {
	ws, _, okURL, _ := newTestServer(t)
	h := ws.Handler()

	cases := map[string]int{
		"/api/history":                                  http.StatusBadRequest,
		"/api/history?endpoint=":                        http.StatusBadRequest,
		"/api/history?endpoint=" + okURL + "&limit=0":   http.StatusBadRequest,
		"/api/history?endpoint=" + okURL + "&limit=abc": http.StatusBadRequest,
		"/api/history?endpoint=" + okURL + "&limit=5":   http.StatusOK,
	}
	for target, want := range cases {
		if rec := get(t, h, target); rec.Code != want {
			t.Errorf("GET %s = %d, want %d (%s)", target, rec.Code, want, strings.TrimSpace(rec.Body.String()))
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/history?endpoint="+okURL, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/history = %d, want 405", rec.Code)
	}
}

// failingStore records writes but fails reads, to check error handling.
type failingStore struct{ *storage.MemoryStore }

func (f *failingStore) GetRecentResults(string, int) ([]checker.CheckResult, error) {
	return nil, errors.New("db down")
}

func TestHistory_StoreError(t *testing.T) {
	cfg := &config.Config{RequestTimeout: time.Second}
	ws := NewWebServer(cfg, checker.NewHTTPChecker(time.Second), &failingStore{storage.NewMemoryStore(1)}, nil, nil)
	rec := get(t, ws.Handler(), "/api/history?endpoint=https://x")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestInsights_RuleBasedWhenAIDisabled(t *testing.T) {
	ws, _, _, badURL := newTestServer(t)
	rec := get(t, ws.Handler(), "/api/insights")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var insights []struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &insights); err != nil {
		t.Fatal(err)
	}
	var sawAlert bool
	for _, in := range insights {
		if in.Type == "alert" && strings.Contains(in.Content, badURL) {
			sawAlert = true
		}
	}
	if !sawAlert {
		t.Errorf("expected an alert naming %s, got %s", badURL, rec.Body.String())
	}
}

func TestEndpoints_AddRemoveAffectsStatus(t *testing.T) {
	ws, _, okURL, badURL := newTestServer(t)
	h := ws.Handler()

	post := func(method, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/endpoints", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := post(http.MethodDelete, `{"url":"`+badURL+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(http.MethodPost, `{"url":"`+okURL+`"}`); rec.Code != http.StatusConflict {
		t.Errorf("duplicate add = %d, want 409", rec.Code)
	}
	if rec := post(http.MethodPost, `{"url":"ftp://nope"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad scheme add = %d, want 400", rec.Code)
	}
	// An over-length URL has to be refused here: SaveResults writes a batch in
	// one transaction, so a URL the store cannot hold would take every other
	// endpoint's result down with it.
	tooLong := "http://example.test/?pad=" + strings.Repeat("z", storage.MaxURLLength)
	if rec := post(http.MethodPost, `{"url":"`+tooLong+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("over-length add = %d, want 400", rec.Code)
	}

	var statuses []EndpointStatus
	if err := json.Unmarshal(get(t, h, "/api/status").Body.Bytes(), &statuses); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].URL != okURL {
		t.Errorf("after delete, status = %+v", statuses)
	}
}
