package checker

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// CheckResult holds the result of checking an endpoint
type CheckResult struct {
	URL          string        `json:"url"`
	StatusCode   int           `json:"status_code"`
	ResponseTime time.Duration `json:"response_time"`
	IsHealthy    bool          `json:"is_healthy"`
	Error        string        `json:"error,omitempty"`
	CheckedAt    time.Time     `json:"checked_at"`
	// WorkerID names the worker process that ran the check. It is empty for
	// checks run outside the coordinated worker loop.
	WorkerID string `json:"worker_id,omitempty"`
}

// HTTPChecker performs HTTP health checks
type HTTPChecker struct {
	client  *http.Client
	timeout time.Duration
}

// NewHTTPChecker creates a new HTTP checker with timeout
func NewHTTPChecker(timeout time.Duration) *HTTPChecker {
	return &HTTPChecker{
		client: &http.Client{
			Timeout: timeout,
		},
		timeout: timeout,
	}
}

// Check performs a health check on the given URL
func (c *HTTPChecker) Check(url string) CheckResult {
	start := time.Now()

	result := CheckResult{
		URL:       url,
		CheckedAt: start,
	}

	resp, err := c.client.Get(url)
	result.ResponseTime = time.Since(start)

	if err != nil {
		result.Error = err.Error()
		result.IsHealthy = false
		return result
	}
	defer resp.Body.Close()
	// Drain (a bounded amount of) the body so the keep-alive connection can be
	// reused by the next check of the same host.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	result.StatusCode = resp.StatusCode
	// Consider 2xx status codes as healthy
	result.IsHealthy = resp.StatusCode >= 200 && resp.StatusCode < 300

	return result
}

// CheckMultiple checks all URLs concurrently and returns results in the same
// order as the input slice. It blocks until every check has finished (or timed out).
func (c *HTTPChecker) CheckMultiple(urls []string) []CheckResult {
	results := make([]CheckResult, len(urls))

	var wg sync.WaitGroup
	wg.Add(len(urls))
	for i, url := range urls {
		go func(i int, u string) {
			defer wg.Done()
			results[i] = c.Check(u)
		}(i, url)
	}
	wg.Wait()

	return results
}
