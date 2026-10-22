package config

import (
	"os"
	"strconv"
	"time"
)

// DefaultLocalDatabaseURL matches the Postgres service in docker-compose.yml.
// It is used by the CLIs when -db is requested and DATABASE_URL is unset; the
// web server never assumes it (persistence there is opt-in via DATABASE_URL).
const DefaultLocalDatabaseURL = "host=localhost port=5432 user=monitor password=password dbname=api_monitor sslmode=disable"

// Config holds all configuration for the API monitor
type Config struct {
	// Database configuration. Empty means persistence is disabled.
	DatabaseURL string

	// Monitoring configuration
	CheckInterval  time.Duration
	RequestTimeout time.Duration
	// MaxConcurrency is the most endpoints one worker leases (and checks
	// concurrently) per round.
	MaxConcurrency int

	// Worker configuration
	WorkerID       string // empty = hostname-pid-random
	WorkerRegion   string
	LeaseDuration  time.Duration
	PollInterval   time.Duration
	EmbeddedWorker bool // run a worker inside cmd/web

	// Web server configuration
	WebPort int

	// AI configuration
	AIEnabled bool
	AIBaseURL string
	AIAPIKey  string
	AIModel   string
	// InsightsTTL is how long /api/insights reuses one answer, so a dashboard
	// polling every few seconds does not issue an LLM call per poll.
	InsightsTTL time.Duration
}

// Load loads configuration from environment variables with defaults
func Load() *Config {
	requestTimeout := getDuration("REQUEST_TIMEOUT", 5*time.Second)
	return &Config{
		// Database
		DatabaseURL: getEnv("DATABASE_URL", ""),

		// Monitoring
		CheckInterval:  getDuration("CHECK_INTERVAL", 15*time.Second),
		RequestTimeout: requestTimeout,
		MaxConcurrency: getInt("MAX_CONCURRENCY", 10),

		// Workers. The default lease leaves room for a full request timeout
		// plus the round trip to record the result.
		WorkerID:       getEnv("WORKER_ID", ""),
		WorkerRegion:   getEnv("WORKER_REGION", "local"),
		LeaseDuration:  getDuration("LEASE_DURATION", 2*requestTimeout+10*time.Second),
		PollInterval:   getDuration("POLL_INTERVAL", time.Second),
		EmbeddedWorker: getBool("EMBEDDED_WORKER", true),

		// Web server
		WebPort: getInt("WEB_PORT", 8080),

		// AI configuration (GPT-OSS)
		AIEnabled:   getBool("AI_ENABLED", true),
		AIBaseURL:   getEnv("AI_BASE_URL", "http://localhost:8000"), // Local GPT-OSS server
		AIAPIKey:    getEnv("AI_API_KEY", "your-api-key-here"),
		AIModel:     getEnv("AI_MODEL", "gpt-oss-20b"),
		InsightsTTL: getDuration("INSIGHTS_TTL", 30*time.Second),
	}
}

// DefaultEndpoints returns the public URLs monitored when none are configured.
// A fresh slice is returned so callers may mutate it.
func DefaultEndpoints() []string {
	return []string{
		"https://api.github.com/users/octocat",
		"https://jsonplaceholder.typicode.com/posts/1",
		"https://httpbin.org/status/200",
		"https://httpbin.org/delay/2",
	}
}

// Helper functions for environment variable parsing
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}

func getBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return defaultValue
}

func getDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return defaultValue
}
