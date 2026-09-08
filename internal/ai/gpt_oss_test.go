package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-monitor/internal/checker"
)

func sampleResults() []checker.CheckResult {
	now := time.Now()
	return []checker.CheckResult{
		{URL: "https://ok.example", StatusCode: 200, ResponseTime: 120 * time.Millisecond, IsHealthy: true, CheckedAt: now},
		{URL: "https://down.example", StatusCode: 503, ResponseTime: 3 * time.Second, IsHealthy: false, Error: "", CheckedAt: now},
	}
}

// fakeOpenAI serves an OpenAI-compatible /v1/chat/completions endpoint whose
// assistant message content is `content`.
func fakeOpenAI(t *testing.T, content string) (*httptest.Server, *ChatCompletionRequest) {
	t.Helper()
	var captured ChatCompletionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			http.Error(w, "unexpected route", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			http.Error(w, "bad auth: "+got, http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(ChatCompletionResponse{
			ID:      "cmpl-1",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   captured.Model,
			Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", Content: content}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func TestAnalyzeEndpoints_ParsesInsights(t *testing.T) {
	content := "Here is my analysis:\n" +
		`[{"title":"Outage","content":"down.example is returning 503","type":"alert","confidence":0.95},` +
		`{"title":"Note","content":"ok.example is fast","type":"bogus","confidence":0.5}]` +
		"\nLet me know if you need more."
	srv, captured := fakeOpenAI(t, content)

	client := NewGPTOSSClient(srv.URL, "test-key", "")
	insights, err := client.AnalyzeEndpoints(context.Background(), sampleResults())
	if err != nil {
		t.Fatalf("AnalyzeEndpoints returned error: %v", err)
	}

	if captured.Model != "gpt-oss-20b" {
		t.Errorf("request model = %q, want default gpt-oss-20b", captured.Model)
	}
	if len(captured.Messages) != 2 || captured.Messages[1].Role != "user" {
		t.Fatalf("expected system+user messages, got %+v", captured.Messages)
	}
	if !strings.Contains(captured.Messages[1].Content, "https://down.example: UNHEALTHY") {
		t.Errorf("prompt does not describe the unhealthy endpoint:\n%s", captured.Messages[1].Content)
	}

	if len(insights) != 2 {
		t.Fatalf("got %d insights, want 2: %+v", len(insights), insights)
	}
	if insights[0].Title != "Outage" || insights[0].Type != "alert" || insights[0].Confidence != 0.95 {
		t.Errorf("insights[0] = %+v", insights[0])
	}
	if insights[1].Type != "info" {
		t.Errorf("invalid type should be normalised to info, got %q", insights[1].Type)
	}
	if insights[0].GeneratedAt.IsZero() {
		t.Errorf("GeneratedAt not set")
	}
}

func TestAnalyzeEndpoints_UnparseableFallsBack(t *testing.T) {
	srv, _ := fakeOpenAI(t, "Sorry, I cannot help with that.")

	client := NewGPTOSSClient(srv.URL, "test-key", "custom-model")
	insights, err := client.AnalyzeEndpoints(context.Background(), sampleResults())
	if err != nil {
		t.Fatalf("unparseable content should not be an error, got %v", err)
	}
	assertFallback(t, insights)
}

func TestAnalyzeEndpoints_ServerErrorFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	client := NewGPTOSSClient(srv.URL, "test-key", "")
	insights, err := client.AnalyzeEndpoints(context.Background(), sampleResults())
	if err == nil {
		t.Fatalf("expected an error from a 500 response")
	}
	if !strings.Contains(err.Error(), "API error 500") {
		t.Errorf("error = %v, want it to mention API error 500", err)
	}
	assertFallback(t, insights)
}

func TestAnalyzeEndpoints_UnreachableFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // connection refused from here on

	client := NewGPTOSSClient(srv.URL, "test-key", "")
	insights, err := client.AnalyzeEndpoints(context.Background(), sampleResults())
	if err == nil {
		t.Fatalf("expected a transport error")
	}
	assertFallback(t, insights)
}

// assertFallback checks that insights look like the rule-based output for
// sampleResults: one down endpoint and one slow endpoint.
func assertFallback(t *testing.T, insights []Insight) {
	t.Helper()
	var sawAlert, sawWarning bool
	for _, in := range insights {
		switch in.Type {
		case "alert":
			sawAlert = true
			if !strings.Contains(in.Content, "https://down.example") {
				t.Errorf("alert should name the down endpoint: %+v", in)
			}
		case "warning":
			sawWarning = true
		}
	}
	if !sawAlert {
		t.Errorf("fallback insights missing alert for the down endpoint: %+v", insights)
	}
	if !sawWarning {
		t.Errorf("fallback insights missing warning for the slow endpoint: %+v", insights)
	}
}

func TestFallbackInsights_AllHealthy(t *testing.T) {
	results := []checker.CheckResult{
		{URL: "https://a", StatusCode: 200, ResponseTime: 100 * time.Millisecond, IsHealthy: true},
		{URL: "https://b", StatusCode: 204, ResponseTime: 200 * time.Millisecond, IsHealthy: true},
	}
	insights := FallbackInsights(results)
	var sawSuccess bool
	for _, in := range insights {
		if in.Type == "alert" || in.Type == "warning" {
			t.Errorf("unexpected %s insight for healthy endpoints: %+v", in.Type, in)
		}
		if in.Type == "success" {
			sawSuccess = true
		}
	}
	if !sawSuccess {
		t.Errorf("expected a success insight, got %+v", insights)
	}
}

func TestFallbackInsights_EmptyDoesNotPanic(t *testing.T) {
	insights := FallbackInsights(nil)
	if len(insights) == 0 {
		t.Errorf("expected at least the generic recommendation")
	}
}
