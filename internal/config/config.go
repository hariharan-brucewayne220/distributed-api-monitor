package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the API monitor
type Config struct {
	// Database configuration
	DatabaseURL string
	
	// Monitoring configuration
	CheckInterval   time.Duration
	RequestTimeout  time.Duration
	MaxConcurrency  int
	
	// Web server configuration
	WebPort int
	
	// AI configuration
	AIEnabled   bool
	AIBaseURL   string
	AIAPIKey    string
	AIModel     string
	
	// Alerting configuration
	AlertingEnabled bool
	SlackWebhook    string
	EmailSMTPHost   string
	EmailSMTPPort   int
	EmailUsername   string
	EmailPassword   string
}

// Load loads configuration from environment variables with defaults
func Load() *Config {
	return &Config{
		// Database
		DatabaseURL: getEnv("DATABASE_URL", "host=localhost port=5432 user=monitor password=password dbname=api_monitor sslmode=disable"),
		
		// Monitoring
		CheckInterval:  getDuration("CHECK_INTERVAL", 15*time.Second),
		RequestTimeout: getDuration("REQUEST_TIMEOUT", 5*time.Second),
		MaxConcurrency: getInt("MAX_CONCURRENCY", 10),
		
		// Web server
		WebPort: getInt("WEB_PORT", 8080),
		
		// AI configuration (GPT-OSS)
		AIEnabled: getBool("AI_ENABLED", true),
		AIBaseURL: getEnv("AI_BASE_URL", "http://localhost:8000"), // Local GPT-OSS server
		AIAPIKey:  getEnv("AI_API_KEY", "your-api-key-here"),
		AIModel:   getEnv("AI_MODEL", "gpt-oss-20b"),
		
		// Alerting
		AlertingEnabled: getBool("ALERTING_ENABLED", false),
		SlackWebhook:    getEnv("SLACK_WEBHOOK", ""),
		EmailSMTPHost:   getEnv("EMAIL_SMTP_HOST", "smtp.gmail.com"),
		EmailSMTPPort:   getInt("EMAIL_SMTP_PORT", 587),
		EmailUsername:   getEnv("EMAIL_USERNAME", ""),
		EmailPassword:   getEnv("EMAIL_PASSWORD", ""),
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