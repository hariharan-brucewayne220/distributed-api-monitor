package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ChatCompletionRequest represents the OpenAI API request format
type ChatCompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
}

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionResponse represents the OpenAI API response format
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

// Choice represents a completion choice
type Choice struct {
	Index   int     `json:"index"`
	Message Message `json:"message"`
}

// Insight represents an AI monitoring insight
type Insight struct {
	Title      string  `json:"title"`
	Content    string  `json:"content"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
}

// MockAI generates realistic monitoring insights
type MockAI struct{}

func (ai *MockAI) generateInsights(prompt string) []Insight {
	prompt = strings.ToLower(prompt)
	
	// Analyze prompt for different scenarios
	if strings.Contains(prompt, "unhealthy") || strings.Contains(prompt, "down") {
		return []Insight{
			{
				Title:      "🚨 Critical Service Disruption Detected",
				Content:    "Multiple endpoints are experiencing downtime. Root cause analysis suggests network connectivity issues or upstream service dependencies. Immediate escalation to infrastructure team recommended.",
				Type:       "alert",
				Confidence: 0.94,
			},
			{
				Title:      "📊 Failure Pattern Analysis",
				Content:    "The outage pattern indicates a cascading failure starting with the delay endpoint. This suggests potential timeout propagation across services. Consider implementing circuit breaker patterns.",
				Type:       "warning",
				Confidence: 0.87,
			},
		}
	}
	
	if strings.Contains(prompt, "slow") || strings.Contains(prompt, "5000ms") || strings.Contains(prompt, "delay") {
		return []Insight{
			{
				Title:      "⚠️ Severe Performance Degradation",
				Content:    "Response times have increased by over 300% from baseline. The delay endpoint is experiencing 5-second timeouts, indicating either network latency issues or server overload.",
				Type:       "warning",
				Confidence: 0.91,
			},
			{
				Title:      "💡 Performance Optimization Strategy",
				Content:    "Implement request timeout controls and consider adding response caching for frequently accessed endpoints. The httpbin delay endpoint suggests external API dependency issues.",
				Type:       "info",
				Confidence: 0.78,
			},
		}
	}
	
	if strings.Contains(prompt, "healthy") && strings.Contains(prompt, "200") {
		return []Insight{
			{
				Title:      "✅ Optimal System Performance",
				Content:    "All monitored endpoints are operating within expected parameters. GitHub API shows excellent stability with sub-250ms response times, demonstrating robust infrastructure.",
				Type:       "success",
				Confidence: 0.96,
			},
			{
				Title:      "📈 Proactive Monitoring Insights",
				Content:    "Current performance metrics indicate 99.8% availability over the monitoring period. Consider this baseline for SLA agreements and capacity planning decisions.",
				Type:       "info",
				Confidence: 0.83,
			},
		}
	}
	
	// Default insights
	return []Insight{
		{
			Title:      "📊 System Health Overview",
			Content:    "Mixed performance indicators observed across monitored endpoints. Some services operating optimally while others show potential for improvement in response time consistency.",
			Type:       "info",
			Confidence: 0.75,
		},
		{
			Title:      "🔍 Monitoring Intelligence",
			Content:    "Recommend implementing automated alerting at 95th percentile thresholds. Current 15-second check interval provides good balance between responsiveness and resource usage.",
			Type:       "info",
			Confidence: 0.68,
		},
	}
}

func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	
	response := map[string]interface{}{
		"status":       "healthy",
		"model":        "gpt-oss-20b-demo",
		"type":         "mock_ai_server", 
		"capabilities": []string{"monitoring_insights", "pattern_analysis", "recommendations"},
		"timestamp":    time.Now().Unix(),
	}
	
	json.NewEncoder(w).Encode(response)
}

func chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	
	// Extract user prompt
	var prompt string
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			prompt = msg.Content
			break
		}
	}
	
	if prompt == "" {
		http.Error(w, "No user message found", http.StatusBadRequest)
		return
	}
	
	// Generate AI insights
	ai := &MockAI{}
	insights := ai.generateInsights(prompt)
	
	// Convert insights to JSON
	insightsJSON, err := json.MarshalIndent(insights, "", "  ")
	if err != nil {
		http.Error(w, "Failed to generate insights", http.StatusInternalServerError)
		return
	}
	
	// Create OpenAI-compatible response
	response := ChatCompletionResponse{
		ID:      "chatcmpl-" + strconv.FormatInt(time.Now().Unix(), 10),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   "gpt-oss-20b",
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: string(insightsJSON),
				},
			},
		},
	}
	
	json.NewEncoder(w).Encode(response)
}

func testHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	w.Header().Set("Content-Type", "application/json")
	
	samplePrompt := `Current endpoint status:
- https://api.github.com/users/octocat: HEALTHY (Status: 200, Response Time: 245ms)
- https://jsonplaceholder.typicode.com/posts/1: HEALTHY (Status: 200, Response Time: 156ms)
- https://httpbin.org/status/200: HEALTHY (Status: 200, Response Time: 892ms)
- https://httpbin.org/delay/2: UNHEALTHY (Status: 0, Response Time: 5000ms, Error: timeout)`
	
	ai := &MockAI{}
	insights := ai.generateInsights(samplePrompt)
	
	response := map[string]interface{}{
		"test":           "success",
		"sample_prompt":  samplePrompt,
		"sample_insights": insights,
		"timestamp":      time.Now().Unix(),
	}
	
	json.NewEncoder(w).Encode(response)
}

func main() {
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/v1/chat/completions", chatCompletionsHandler)
	http.HandleFunc("/demo/test", testHandler)
	
	port := 8000
	fmt.Printf("🚀 Mock GPT-OSS AI Server starting...\n")
	fmt.Printf("🤖 Simulating OpenAI GPT-OSS-20B for monitoring insights\n")
	fmt.Printf("🌐 Server running on http://localhost:%d\n", port)
	fmt.Printf("\n📡 Available endpoints:\n")
	fmt.Printf("   - GET  /health              - Health check\n")
	fmt.Printf("   - POST /v1/chat/completions - OpenAI-compatible API\n")
	fmt.Printf("   - GET  /demo/test           - Test sample insights\n")
	fmt.Printf("\n🧪 Test the server:\n")
	fmt.Printf("   curl http://localhost:%d/health\n", port)
	fmt.Printf("   curl http://localhost:%d/demo/test\n", port)
	fmt.Printf("\n✅ Ready for API Monitor integration!\n\n")
	
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}