package main

import (
	"flag"
	"fmt"
	"log"

	"api-monitor/internal/storage"
)

func main() {
	url := flag.String("url", "", "URL to query results for")
	limit := flag.Int("limit", 10, "Number of recent results to fetch")
	flag.Parse()

	if *url == "" {
		log.Fatal("Please provide a URL with -url flag")
	}

	fmt.Printf("🔍 Querying results for: %s\n\n", *url)

	// Connect to database
	connectionString := "host=localhost port=5432 user=monitor password=password dbname=api_monitor sslmode=disable"
	store, err := storage.NewPostgresStore(connectionString)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer store.Close()

	// Get recent results
	results, err := store.GetRecentResults(*url, *limit)
	if err != nil {
		log.Fatalf("Failed to query results: %v", err)
	}

	if len(results) == 0 {
		fmt.Println("No results found for this URL")
		return
	}

	fmt.Printf("📊 Found %d recent results:\n\n", len(results))

	for i, result := range results {
		status := "✅ HEALTHY"
		if !result.IsHealthy {
			status = "❌ UNHEALTHY"
		}

		fmt.Printf("%d. %s\n", i+1, status)
		fmt.Printf("   Time: %s\n", result.CheckedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("   Status: %d | Response Time: %v\n", 
			result.StatusCode, result.ResponseTime)

		if result.Error != "" {
			fmt.Printf("   Error: %s\n", result.Error)
		}
		fmt.Println()
	}

	// Calculate some basic statistics
	var totalResponseTime int64
	var healthyCount int
	
	for _, result := range results {
		totalResponseTime += result.ResponseTime.Milliseconds()
		if result.IsHealthy {
			healthyCount++
		}
	}

	avgResponseTime := totalResponseTime / int64(len(results))
	uptime := (float64(healthyCount) / float64(len(results))) * 100

	fmt.Printf("📈 Statistics:\n")
	fmt.Printf("   Average Response Time: %dms\n", avgResponseTime)
	fmt.Printf("   Uptime: %.1f%% (%d/%d checks)\n", uptime, healthyCount, len(results))
}