package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"api-monitor/internal/ai"
	"api-monitor/internal/checker"
	"api-monitor/internal/config"
)

type WebServer struct {
	checker   *checker.HTTPChecker
	aiClient  *ai.GPTOSSClient
	urls      []string
	urlsMutex sync.RWMutex
	config    *config.Config
}

type EndpointStatus struct {
	URL          string        `json:"url"`
	IsHealthy    bool          `json:"isHealthy"`
	StatusCode   int           `json:"statusCode"`
	ResponseTime time.Duration `json:"responseTime"`
	LastChecked  time.Time     `json:"lastChecked"`
	Error        string        `json:"error,omitempty"`
}

type EndpointRequest struct {
	URL string `json:"url"`
}

func NewWebServer() *WebServer {
	cfg := config.Load()
	
	var aiClient *ai.GPTOSSClient
    if cfg.AIEnabled {
        aiClient = ai.NewGPTOSSClient(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
    }
	
	return &WebServer{
		checker:  checker.NewHTTPChecker(cfg.RequestTimeout),
		aiClient: aiClient,
		config:   cfg,
		urls: []string{
			"https://api.github.com/users/octocat",
			"https://jsonplaceholder.typicode.com/posts/1",
			"https://httpbin.org/status/200",
			"https://httpbin.org/delay/2",
		},
	}
}

func (ws *WebServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	ws.urlsMutex.RLock()
	urls := make([]string, len(ws.urls))
	copy(urls, ws.urls)
	ws.urlsMutex.RUnlock()

	results := ws.checker.CheckMultiple(urls)
	
	var statuses []EndpointStatus
	for _, result := range results {
		status := EndpointStatus{
			URL:          result.URL,
			IsHealthy:    result.IsHealthy,
			StatusCode:   result.StatusCode,
			ResponseTime: result.ResponseTime,
			LastChecked:  result.CheckedAt,
			Error:        result.Error,
		}
		statuses = append(statuses, status)
	}

	json.NewEncoder(w).Encode(statuses)
}

func (ws *WebServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/index.html")
}

func (ws *WebServer) handleAIInsights(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get current status
	ws.urlsMutex.RLock()
	urls := make([]string, len(ws.urls))
	copy(urls, ws.urls)
	ws.urlsMutex.RUnlock()

	results := ws.checker.CheckMultiple(urls)
	
	var insights []ai.Insight
	
	// Try AI-powered insights first
	if ws.aiClient != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		
		aiInsights, err := ws.aiClient.AnalyzeEndpoints(ctx, results)
		if err != nil {
			log.Printf("AI insights failed: %v", err)
			// Fall back to rule-based insights
			insights = ws.convertLegacyInsights(ws.generateInsights(results))
		} else {
			insights = aiInsights
		}
	} else {
		// Use rule-based insights if AI is disabled
		insights = ws.convertLegacyInsights(ws.generateInsights(results))
	}
	
	json.NewEncoder(w).Encode(insights)
}

type AIInsight struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Type    string `json:"type"` // "alert", "warning", "info", "success"
}

func (ws *WebServer) generateInsights(results []checker.CheckResult) []AIInsight {
	var insights []AIInsight
	
	// Count unhealthy endpoints
	unhealthy := 0
	var unhealthyURLs []string
	totalResponseTime := time.Duration(0)
	slowEndpoints := 0
	
	for _, result := range results {
		if !result.IsHealthy {
			unhealthy++
			unhealthyURLs = append(unhealthyURLs, result.URL)
		}
		totalResponseTime += result.ResponseTime
		if result.ResponseTime > 2*time.Second {
			slowEndpoints++
		}
	}
	
	avgResponseTime := totalResponseTime / time.Duration(len(results))
	
	// Generate insights based on analysis
	if unhealthy > 0 {
		insights = append(insights, AIInsight{
			Title:   "🚨 Service Disruption Detected",
			Content: fmt.Sprintf("%d endpoint(s) are currently down. Immediate attention required for: %v", unhealthy, unhealthyURLs),
			Type:    "alert",
		})
	}
	
	if slowEndpoints > 0 {
		insights = append(insights, AIInsight{
			Title:   "⚠️ Performance Degradation Alert",
			Content: fmt.Sprintf("%d endpoint(s) showing elevated response times (>2s). This may indicate network congestion or server load issues.", slowEndpoints),
			Type:    "warning",
		})
	}
	
	if avgResponseTime < 500*time.Millisecond && unhealthy == 0 {
		insights = append(insights, AIInsight{
			Title:   "✅ Optimal System Performance",
			Content: fmt.Sprintf("All endpoints healthy with excellent average response time of %v. System operating within optimal parameters.", avgResponseTime.Round(time.Millisecond)),
			Type:    "success",
		})
	}
	
	// Predictive insights
	insights = append(insights, AIInsight{
		Title:   "💡 Proactive Recommendation",
		Content: "Based on current patterns, consider implementing automated scaling for endpoints with response times consistently above 1.5s to maintain optimal user experience.",
		Type:    "info",
	})
	
	// Pattern analysis
	if avgResponseTime > 1*time.Second {
		insights = append(insights, AIInsight{
			Title:   "📊 Pattern Analysis",
			Content: fmt.Sprintf("Average response time of %v suggests potential bottlenecks. Recommend investigating database query optimization and caching strategies.", avgResponseTime.Round(time.Millisecond)),
			Type:    "info",
		})
	}
	
	return insights
}

// convertLegacyInsights converts old AIInsight format to new ai.Insight format
func (ws *WebServer) convertLegacyInsights(legacyInsights []AIInsight) []ai.Insight {
	insights := make([]ai.Insight, len(legacyInsights))
	for i, legacy := range legacyInsights {
		insights[i] = ai.Insight{
			Title:       legacy.Title,
			Content:     legacy.Content,
			Type:        legacy.Type,
			Confidence:  0.8, // Default confidence for rule-based insights
			GeneratedAt: time.Now(),
		}
	}
	return insights
}

func (ws *WebServer) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch r.Method {
	case "GET":
		ws.urlsMutex.RLock()
		urls := make([]string, len(ws.urls))
		copy(urls, ws.urls)
		ws.urlsMutex.RUnlock()
		
		json.NewEncoder(w).Encode(map[string][]string{"urls": urls})

	case "POST":
		var req EndpointRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		// Validate URL
		url := strings.TrimSpace(req.URL)
		if url == "" {
			http.Error(w, "URL is required", http.StatusBadRequest)
			return
		}

		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			http.Error(w, "URL must start with http:// or https://", http.StatusBadRequest)
			return
		}

		// Add URL
		ws.urlsMutex.Lock()
		// Check if URL already exists
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

	case "DELETE":
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

		// Remove URL
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

func main() {
	ws := NewWebServer()

	// Serve static files
	http.HandleFunc("/", ws.handleDashboard)
	http.HandleFunc("/api/status", ws.handleStatus)
	http.HandleFunc("/api/insights", ws.handleAIInsights)
	http.HandleFunc("/api/endpoints", ws.handleEndpoints)

	port := ws.config.WebPort
	fmt.Printf("🌐 Web dashboard starting on http://localhost:%d\n", port)
	fmt.Printf("📊 API endpoints:\n")
	fmt.Printf("   - GET /               - Web dashboard\n")
	fmt.Printf("   - GET /api/status     - Current endpoint status\n")
	fmt.Printf("   - GET /api/insights   - AI-powered insights\n")
	fmt.Printf("   - POST/DELETE /api/endpoints - Manage monitored URLs\n")
	
	if ws.aiClient != nil {
		fmt.Printf("🤖 AI insights powered by GPT-OSS\n")
	} else {
		fmt.Printf("📋 Using rule-based insights (AI disabled)\n")
	}
	
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}