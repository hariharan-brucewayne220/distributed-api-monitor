package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"api-monitor/internal/ai"
	"api-monitor/internal/checker"
	"api-monitor/internal/config"
	"api-monitor/internal/storage"
	"api-monitor/internal/worker"
)

const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 1000

	// workerStaleAfter is how long since its last heartbeat a worker is still
	// reported alive. It allows for a heartbeat delayed by a slow batch.
	workerStaleAfter = 30 * time.Second
)

// WebServer serves the dashboard and JSON API. It never runs checks itself:
// workers (embedded in this process, or separate cmd/monitor processes) write
// results to the coordinator, and the API reads them back. That keeps the load
// on monitored endpoints independent of how many dashboards are open and how
// many web replicas are running.
type WebServer struct {
	aiClient *ai.GPTOSSClient
	coord    storage.Coordinator
	config   *config.Config

	insightsMu  sync.Mutex
	insightsAt  time.Time
	insightsVal []ai.Insight
}

// EndpointStatus is the JSON shape used by /api/status and /api/history.
type EndpointStatus struct {
	URL          string        `json:"url"`
	IsHealthy    bool          `json:"isHealthy"`
	StatusCode   int           `json:"statusCode"`
	ResponseTime time.Duration `json:"responseTime"`
	LastChecked  time.Time     `json:"lastChecked"`
	Error        string        `json:"error,omitempty"`
	CheckedBy    string        `json:"checkedBy,omitempty"`
}

// WorkerStatus is the JSON shape returned by /api/workers.
type WorkerStatus struct {
	ID             string    `json:"id"`
	Region         string    `json:"region"`
	StartedAt      time.Time `json:"startedAt"`
	LastHeartbeat  time.Time `json:"lastHeartbeat"`
	SinceHeartbeat float64   `json:"secondsSinceHeartbeat"`
	Alive          bool      `json:"alive"`
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
func NewWebServer(cfg *config.Config, coord storage.Coordinator, aiClient *ai.GPTOSSClient) *WebServer {
	return &WebServer{
		aiClient: aiClient,
		coord:    coord,
		config:   cfg,
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
	mux.HandleFunc("/api/workers", ws.handleWorkers)
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
		CheckedBy:    result.WorkerID,
	}
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

	results, err := ws.coord.LatestResults()
	if err != nil {
		log.Printf("latest results query failed: %v", err)
		http.Error(w, "failed to read status", http.StatusInternalServerError)
		return
	}
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

	results, err := ws.coord.GetRecentResults(endpoint, limit)
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

	insights, err := ws.insights(r.Context())
	if err != nil {
		log.Printf("latest results query failed: %v", err)
		http.Error(w, "failed to read status", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(insights)
}

// insights returns insights for the latest results, reusing the previous
// answer for InsightsTTL. The lock is held while generating so that requests
// arriving during a slow LLM call wait for its answer instead of each starting
// their own.
func (ws *WebServer) insights(ctx context.Context) ([]ai.Insight, error) {
	ws.insightsMu.Lock()
	defer ws.insightsMu.Unlock()

	if ttl := ws.config.InsightsTTL; ttl > 0 && ws.insightsVal != nil && time.Since(ws.insightsAt) < ttl {
		return ws.insightsVal, nil
	}

	results, err := ws.coord.LatestResults()
	if err != nil {
		return nil, err
	}

	var insights []ai.Insight
	if ws.aiClient != nil {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
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

	ws.insightsVal, ws.insightsAt = insights, time.Now()
	return insights, nil
}

// handleWorkers serves GET /api/workers: every worker that has sent a
// heartbeat in the last hour, and whether it is still considered alive.
func (ws *WebServer) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if setCORS(w, r, "GET, OPTIONS") {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	workers, err := ws.coord.ListWorkers()
	if err != nil {
		log.Printf("workers query failed: %v", err)
		http.Error(w, "failed to read workers", http.StatusInternalServerError)
		return
	}
	out := make([]WorkerStatus, 0, len(workers))
	for _, wk := range workers {
		out = append(out, WorkerStatus{
			ID:             wk.ID,
			Region:         wk.Region,
			StartedAt:      wk.StartedAt,
			LastHeartbeat:  wk.LastHeartbeat,
			SinceHeartbeat: wk.SinceHeartbeat.Seconds(),
			Alive:          wk.SinceHeartbeat < workerStaleAfter,
		})
	}
	json.NewEncoder(w).Encode(out)
}

func (ws *WebServer) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	if setCORS(w, r, "GET, POST, DELETE, OPTIONS") {
		return
	}

	switch r.Method {
	case http.MethodGet:
		urls, err := ws.coord.ListEndpoints()
		if err != nil {
			log.Printf("list endpoints failed: %v", err)
			http.Error(w, "failed to list endpoints", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string][]string{"urls": urls})

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
		// Reject here rather than at INSERT time: results are written in
		// batches, one transaction each, so a URL the store cannot hold would
		// discard the results for every other endpoint checked alongside it.
		if len(url) > storage.MaxURLLength {
			http.Error(w, fmt.Sprintf("URL must be at most %d characters", storage.MaxURLLength), http.StatusBadRequest)
			return
		}

		if err := ws.coord.AddEndpoint(url, ws.config.CheckInterval); err != nil {
			if errors.Is(err, storage.ErrEndpointExists) {
				http.Error(w, "URL already being monitored", http.StatusConflict)
				return
			}
			log.Printf("add endpoint %s failed: %v", url, err)
			http.Error(w, "failed to add endpoint", http.StatusInternalServerError)
			return
		}

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

		if err := ws.coord.RemoveEndpoint(url); err != nil {
			if errors.Is(err, storage.ErrEndpointNotFound) {
				http.Error(w, "URL not found", http.StatusNotFound)
				return
			}
			log.Printf("remove endpoint %s failed: %v", url, err)
			http.Error(w, "failed to remove endpoint", http.StatusInternalServerError)
			return
		}

		log.Printf("Removed endpoint: %s", url)
		json.NewEncoder(w).Encode(map[string]string{"message": "Endpoint removed successfully"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// openCoordinator returns a Postgres-backed coordinator when DATABASE_URL is
// set and an in-memory one otherwise.
func openCoordinator(cfg *config.Config) (storage.Coordinator, error) {
	if cfg.DatabaseURL == "" {
		log.Printf("DATABASE_URL not set: single-process mode, endpoints and history are kept in memory for this process only")
		return storage.NewMemoryStore(storage.DefaultMemoryCapacity), nil
	}
	store, err := storage.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to DATABASE_URL: %w", err)
	}
	log.Printf("cluster mode: endpoints, leases and results are shared through Postgres")
	return store, nil
}

func main() {
	cfg := config.Load()

	coord, err := openCoordinator(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer coord.Close()

	if err := coord.SeedEndpoints(config.DefaultEndpoints(), cfg.CheckInterval); err != nil {
		log.Fatalf("seed endpoints: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.EmbeddedWorker {
		w := &worker.Worker{
			ID:            cfg.WorkerID,
			Region:        cfg.WorkerRegion,
			Coord:         coord,
			Checker:       checker.NewHTTPChecker(cfg.RequestTimeout),
			BatchSize:     cfg.MaxConcurrency,
			LeaseDuration: cfg.LeaseDuration,
			PollInterval:  cfg.PollInterval,
		}
		go w.Run(ctx)
	} else if cfg.DatabaseURL == "" {
		log.Printf("warning: EMBEDDED_WORKER=false without DATABASE_URL means nothing will ever check the endpoints")
	} else {
		log.Printf("embedded worker off: relying on separate cmd/monitor workers")
	}

	var aiClient *ai.GPTOSSClient
	if cfg.AIEnabled {
		aiClient = ai.NewGPTOSSClient(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
	}

	ws := NewWebServer(cfg, coord, aiClient)

	port := cfg.WebPort
	fmt.Printf("Web dashboard starting on http://localhost:%d\n", port)
	fmt.Printf("API endpoints:\n")
	fmt.Printf("   - GET /                 - Web dashboard\n")
	fmt.Printf("   - GET /api/status       - Latest result per endpoint\n")
	fmt.Printf("   - GET /api/history      - Recent results (?endpoint=<url>&limit=N)\n")
	fmt.Printf("   - GET /api/insights     - AI-powered insights\n")
	fmt.Printf("   - GET/POST/DELETE /api/endpoints - Manage monitored URLs\n")
	fmt.Printf("   - GET /api/workers      - Workers and their heartbeats\n")

	if aiClient != nil {
		fmt.Printf("AI insights via %s (%s)\n", cfg.AIBaseURL, cfg.AIModel)
	} else {
		fmt.Printf("Using rule-based insights (AI disabled)\n")
	}

	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: ws.Handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
