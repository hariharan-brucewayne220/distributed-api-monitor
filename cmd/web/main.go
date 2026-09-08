package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"api-monitor/internal/ai"
	"api-monitor/internal/checker"
	"api-monitor/internal/config"
	"api-monitor/internal/storage"
)

const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 1000
)

// WebServer serves the dashboard and JSON API. Every batch of checks it runs
// is written to store, which is Postgres when DATABASE_URL is set and an
// in-process ring buffer otherwise.
type WebServer struct {
	checker   *checker.HTTPChecker
	aiClient  *ai.GPTOSSClient
	store     storage.Store
	urls      []string
	urlsMutex sync.RWMutex
	config    *config.Config
}

// EndpointStatus is the JSON shape used by /api/status and /api/history.
type EndpointStatus struct {
	URL          string        `json:"url"`
	IsHealthy    bool          `json:"isHealthy"`
	StatusCode   int           `json:"statusCode"`
	ResponseTime time.Duration `json:"responseTime"`
	LastChecked  time.Time     `json:"lastChecked"`
	Error        string        `json:"error,omitempty"`
}

// HistoryResponse is the JSON shape returned by /api/history.
type HistoryResponse struct {
	Endpoint string           `json:"endpoint"`
	Count    int              `json:"count"`
	Results  []EndpointStatus `json:"results"`
}

type EndpointRequest struct {
	URL string `json:"url"`
}

// NewWebServer wires a server from its collaborators. aiClient may be nil to
// use rule-based insights only.
func NewWebServer(cfg *config.Config, chk *checker.HTTPChecker, store storage.Store, aiClient *ai.GPTOSSClient, urls []string) *WebServer {
	return &WebServer{
		checker:  chk,
		aiClient: aiClient,
		store:    store,
		config:   cfg,
		urls:     append([]string(nil), urls...),
	}
}

// Handler returns the HTTP routes for the server.
func (ws *WebServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleDashboard)
	mux.HandleFunc("/api/status", ws.handleStatus)
	mux.HandleFunc("/api/history", ws.handleHistory)
	mux.HandleFunc("/api/insights", ws.handleAIInsights)
	mux.HandleFunc("/api/endpoints", ws.handleEndpoints)
	return mux
}

func toStatus(result checker.CheckResult) EndpointStatus {
	return EndpointStatus{
		URL:          result.URL,
		IsHealthy:    result.IsHealthy,
		StatusCode:   result.StatusCode,
		ResponseTime: result.ResponseTime,
		LastChecked:  result.CheckedAt,
		Error:        result.Error,
	}
}

func (ws *WebServer) snapshotURLs() []string {
	ws.urlsMutex.RLock()
	defer ws.urlsMutex.RUnlock()
	return append([]string(nil), ws.urls...)
}

// runChecks checks every monitored URL concurrently and persists the batch.
func (ws *WebServer) runChecks() []checker.CheckResult {
	results := ws.checker.CheckMultiple(ws.snapshotURLs())
	if len(results) > 0 {
		if err := ws.store.SaveResults(results); err != nil {
			log.Printf("failed to persist %d check results: %v", len(results), err)
		}
	}
	return results
}

// setCORS applies the permissive CORS headers used by every JSON route and
// answers preflight requests. It returns true when the request was a preflight
// that has already been handled.
func setCORS(w http.ResponseWriter, r *http.Request, methods string) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", methods)
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

func (ws *WebServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if setCORS(w, r, "GET, OPTIONS") {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	results := ws.runChecks()
	statuses := make([]EndpointStatus, 0, len(results))
	for _, result := range results {
		statuses = append(statuses, toStatus(result))
	}
	json.NewEncoder(w).Encode(statuses)
}

// handleHistory serves GET /api/history?endpoint=<url>&limit=N with the most
// recent stored results for one endpoint, newest first.
func (ws *WebServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	if setCORS(w, r, "GET, OPTIONS") {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	if endpoint == "" {
		http.Error(w, "endpoint query parameter is required", http.StatusBadRequest)
		return
	}

	limit := defaultHistoryLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = n
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}

	results, err := ws.store.GetRecentResults(endpoint, limit)
	if err != nil {
		log.Printf("history query for %s failed: %v", endpoint, err)
		http.Error(w, "failed to read history", http.StatusInternalServerError)
		return
	}

	resp := HistoryResponse{Endpoint: endpoint, Count: len(results), Results: make([]EndpointStatus, 0, len(results))}
	for _, result := range results {
		resp.Results = append(resp.Results, toStatus(result))
	}
	json.NewEncoder(w).Encode(resp)
}

func (ws *WebServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/index.html")
}

func (ws *WebServer) handleAIInsights(w http.ResponseWriter, r *http.Request) {
	if setCORS(w, r, "GET, OPTIONS") {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	results := ws.runChecks()

	var insights []ai.Insight
	if ws.aiClient != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		aiInsights, err := ws.aiClient.AnalyzeEndpoints(ctx, results)
		if err != nil {
			// AnalyzeEndpoints already returns rule-based insights on failure.
			log.Printf("AI insights failed: %v", err)
		}
		insights = aiInsights
	} else {
		insights = ai.FallbackInsights(results)
	}

	json.NewEncoder(w).Encode(insights)
}

func (ws *WebServer) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	if setCORS(w, r, "GET, POST, DELETE, OPTIONS") {
		return
	}

	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(map[string][]string{"urls": ws.snapshotURLs()})

	case http.MethodPost:
		var req EndpointRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		url := strings.TrimSpace(req.URL)
		if url == "" {
			http.Error(w, "URL is required", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			http.Error(w, "URL must start with http:// or https://", http.StatusBadRequest)
			return
		}
		// Reject here rather than at INSERT time: SaveResults writes a batch in
		// one transaction, so a URL the store cannot hold would discard the
		// results for every other endpoint checked alongside it.
		if len(url) > storage.MaxURLLength {
			http.Error(w, fmt.Sprintf("URL must be at most %d characters", storage.MaxURLLength), http.StatusBadRequest)
			return
		}

		ws.urlsMutex.Lock()
		for _, existingURL := range ws.urls {
			if existingURL == url {
				ws.urlsMutex.Unlock()
				http.Error(w, "URL already being monitored", http.StatusConflict)
				return
			}
		}
		ws.urls = append(ws.urls, url)
		ws.urlsMutex.Unlock()

		log.Printf("Added endpoint: %s", url)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"message": "Endpoint added successfully"})

	case http.MethodDelete:
		var req EndpointRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		url := strings.TrimSpace(req.URL)
		if url == "" {
			http.Error(w, "URL is required", http.StatusBadRequest)
			return
		}

		ws.urlsMutex.Lock()
		found := false
		for i, existingURL := range ws.urls {
			if existingURL == url {
				ws.urls = append(ws.urls[:i], ws.urls[i+1:]...)
				found = true
				break
			}
		}
		ws.urlsMutex.Unlock()

		if !found {
			http.Error(w, "URL not found", http.StatusNotFound)
			return
		}

		log.Printf("Removed endpoint: %s", url)
		json.NewEncoder(w).Encode(map[string]string{"message": "Endpoint removed successfully"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// openStore returns a Postgres-backed store when DATABASE_URL is set and an
// in-memory store otherwise.
func openStore(cfg *config.Config) (storage.Store, error) {
	if cfg.DatabaseURL == "" {
		log.Printf("DATABASE_URL not set: persistence is off, history is kept in memory for this process only")
		return storage.NewMemoryStore(storage.DefaultMemoryCapacity), nil
	}
	store, err := storage.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to DATABASE_URL: %w", err)
	}
	log.Printf("persistence on: check results are written to Postgres")
	return store, nil
}

func main() {
	cfg := config.Load()

	store, err := openStore(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	var aiClient *ai.GPTOSSClient
	if cfg.AIEnabled {
		aiClient = ai.NewGPTOSSClient(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
	}

	ws := NewWebServer(cfg, checker.NewHTTPChecker(cfg.RequestTimeout), store, aiClient, config.DefaultEndpoints())

	port := cfg.WebPort
	fmt.Printf("Web dashboard starting on http://localhost:%d\n", port)
	fmt.Printf("API endpoints:\n")
	fmt.Printf("   - GET /                 - Web dashboard\n")
	fmt.Printf("   - GET /api/status       - Check all endpoints now\n")
	fmt.Printf("   - GET /api/history      - Recent results (?endpoint=<url>&limit=N)\n")
	fmt.Printf("   - GET /api/insights     - AI-powered insights\n")
	fmt.Printf("   - GET/POST/DELETE /api/endpoints - Manage monitored URLs\n")

	if aiClient != nil {
		fmt.Printf("AI insights via %s (%s)\n", cfg.AIBaseURL, cfg.AIModel)
	} else {
		fmt.Printf("Using rule-based insights (AI disabled)\n")
	}

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), ws.Handler()))
}
