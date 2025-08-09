package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"api-monitor/internal/checker"
)

// GPTOSSClient handles interactions with OpenAI's GPT-OSS model
type GPTOSSClient struct {
	baseURL    string
	apiKey     string
	model      string
	client     *http.Client
	maxTokens  int
	temperature float64
}

// Insight represents an AI-generated monitoring insight
type Insight struct {
	Title       string    `json:"title"`
	Content     string    `json:"content"`
	Type        string    `json:"type"`        // "alert", "warning", "info", "success"
	Confidence  float64   `json:"confidence"`  // 0.0 to 1.0
	GeneratedAt time.Time `json:"generatedAt"`
}

// ChatCompletionRequest represents the request structure for GPT-OSS
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

// ChatCompletionResponse represents the response from GPT-OSS
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

// NewGPTOSSClient creates a new GPT-OSS client
func NewGPTOSSClient(baseURL, apiKey, model string) *GPTOSSClient {
    effectiveModel := strings.TrimSpace(model)
    if effectiveModel == "" {
        effectiveModel = "gpt-oss-20b"
    }
    return &GPTOSSClient{
        baseURL:     baseURL,
        apiKey:      apiKey,
        model:       effectiveModel,
        client:      &http.Client{Timeout: 30 * time.Second},
        maxTokens:   512,
        temperature: 0.3, // Lower temperature for more consistent analytical responses
    }
}

// AnalyzeEndpoints generates AI insights from endpoint monitoring data
func (c *GPTOSSClient) AnalyzeEndpoints(ctx context.Context, results []checker.CheckResult) ([]Insight, error) {
	prompt := c.buildAnalysisPrompt(results)
	
	response, err := c.complete(ctx, prompt)
	if err != nil {
		// Fallback to rule-based insights if AI fails
		return c.fallbackInsights(results), fmt.Errorf("AI analysis failed, using fallback: %w", err)
	}
	
	insights := c.parseInsights(response)
	if len(insights) == 0 {
		// Fallback if parsing fails
		return c.fallbackInsights(results), nil
	}
	
	return insights, nil
}

// buildAnalysisPrompt creates a structured prompt for endpoint analysis
func (c *GPTOSSClient) buildAnalysisPrompt(results []checker.CheckResult) string {
	var sb strings.Builder
	
	sb.WriteString("You are an expert system administrator analyzing API endpoint monitoring data. ")
	sb.WriteString("Provide 2-4 concise insights in JSON format with title, content, type (alert/warning/info/success), and confidence (0.0-1.0).\n\n")
	sb.WriteString("Current endpoint status:\n")
	
	for _, result := range results {
		status := "HEALTHY"
		if !result.IsHealthy {
			status = "UNHEALTHY"
		}
		
		sb.WriteString(fmt.Sprintf("- %s: %s (Status: %d, Response Time: %v, Error: %s)\n",
			result.URL, status, result.StatusCode, result.ResponseTime.Round(time.Millisecond), result.Error))
	}
	
	sb.WriteString("\nProvide insights as JSON array: [{\"title\":\"...\",\"content\":\"...\",\"type\":\"alert|warning|info|success\",\"confidence\":0.9}]\n")
	sb.WriteString("Focus on:\n")
	sb.WriteString("1. Immediate issues requiring attention\n")
	sb.WriteString("2. Performance trends and patterns\n")
	sb.WriteString("3. Proactive recommendations\n")
	sb.WriteString("4. System health summary\n")
	
	return sb.String()
}

// complete sends a completion request to GPT-OSS
func (c *GPTOSSClient) complete(ctx context.Context, prompt string) (string, error) {
	request := ChatCompletionRequest{
		Model: c.model,
		Messages: []Message{
			{
				Role:    "system",
				Content: "You are a monitoring system AI assistant. Respond only with valid JSON.",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		MaxTokens:   c.maxTokens,
		Temperature: c.temperature,
	}
	
	jsonData, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}
	
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}
	
	var response ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	
	return response.Choices[0].Message.Content, nil
}

// parseInsights extracts insights from AI response
func (c *GPTOSSClient) parseInsights(response string) []Insight {
	// Find JSON array in response
	start := strings.Index(response, "[")
	end := strings.LastIndex(response, "]")
	
	if start == -1 || end == -1 || start >= end {
		return nil
	}
	
	jsonStr := response[start : end+1]
	
	var rawInsights []struct {
		Title      string  `json:"title"`
		Content    string  `json:"content"`
		Type       string  `json:"type"`
		Confidence float64 `json:"confidence"`
	}
	
	if err := json.Unmarshal([]byte(jsonStr), &rawInsights); err != nil {
		return nil
	}
	
	insights := make([]Insight, len(rawInsights))
	for i, raw := range rawInsights {
		insights[i] = Insight{
			Title:       raw.Title,
			Content:     raw.Content,
			Type:        c.validateType(raw.Type),
			Confidence:  raw.Confidence,
			GeneratedAt: time.Now(),
		}
	}
	
	return insights
}

// validateType ensures insight type is valid
func (c *GPTOSSClient) validateType(t string) string {
	validTypes := map[string]bool{
		"alert":   true,
		"warning": true,
		"info":    true,
		"success": true,
	}
	
	if validTypes[t] {
		return t
	}
	return "info" // default fallback
}

// fallbackInsights provides rule-based insights when AI is unavailable
func (c *GPTOSSClient) fallbackInsights(results []checker.CheckResult) []Insight {
	var insights []Insight
	
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
	
	if unhealthy > 0 {
		insights = append(insights, Insight{
			Title:       "🚨 Service Disruption Detected",
			Content:     fmt.Sprintf("%d endpoint(s) are currently down: %s", unhealthy, strings.Join(unhealthyURLs, ", ")),
			Type:        "alert",
			Confidence:  1.0,
			GeneratedAt: time.Now(),
		})
	}
	
	if slowEndpoints > 0 {
		insights = append(insights, Insight{
			Title:       "⚠️ Performance Issues",
			Content:     fmt.Sprintf("%d endpoint(s) showing elevated response times (>2s). Consider investigating server load or network issues.", slowEndpoints),
			Type:        "warning",
			Confidence:  0.9,
			GeneratedAt: time.Now(),
		})
	}
	
	if avgResponseTime < 500*time.Millisecond && unhealthy == 0 {
		insights = append(insights, Insight{
			Title:       "✅ System Health Excellent",
			Content:     fmt.Sprintf("All endpoints healthy with optimal average response time of %v.", avgResponseTime.Round(time.Millisecond)),
			Type:        "success",
			Confidence:  0.95,
			GeneratedAt: time.Now(),
		})
	}
	
	insights = append(insights, Insight{
		Title:       "💡 Monitoring Recommendation",
		Content:     "Consider setting up automated alerts for response times >3s and implementing health check redundancy across multiple regions.",
		Type:        "info",
		Confidence:  0.8,
		GeneratedAt: time.Now(),
	})
	
	return insights
}