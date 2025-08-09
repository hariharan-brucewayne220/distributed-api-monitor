package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"api-monitor/internal/checker"
	"api-monitor/internal/storage"
)

func main() {
	// Command line flags
	useDB := flag.Bool("db", false, "Use database storage")
	flag.Parse()

	fmt.Println("🚀 Starting API Monitor...")
	
	// Create checker with 5 second timeout
	httpChecker := checker.NewHTTPChecker(5 * time.Second)
	
	// Setup database if requested
	var store *storage.PostgresStore
	if *useDB {
		fmt.Println("📊 Connecting to database...")
		connectionString := "host=localhost port=5432 user=monitor password=password dbname=api_monitor sslmode=disable"
		
		var err error
		store, err = storage.NewPostgresStore(connectionString)
		if err != nil {
			log.Fatalf("Failed to connect to database: %v", err)
		}
		defer store.Close()
		fmt.Println("✅ Database connected!")
	}
	
	// URLs to monitor (we'll start with public APIs)
	urls := []string{
		"https://api.github.com/users/octocat",
		"https://jsonplaceholder.typicode.com/posts/1", 
		"https://httpbin.org/status/200",
		"https://httpbin.org/delay/2", // Reduced to 2 seconds
	}
	
	fmt.Printf("📡 Monitoring %d endpoints...\n\n", len(urls))
	
	// Run checks every 15 seconds
	for {
		results := httpChecker.CheckMultiple(urls)
		
		// Save to database if enabled
		if store != nil {
			if err := store.SaveResults(results); err != nil {
				log.Printf("Failed to save results: %v", err)
			} else {
				fmt.Println("💾 Results saved to database")
			}
		}
		
		fmt.Printf("=== Check Results at %s ===\n", time.Now().Format("15:04:05"))
		
		for _, result := range results {
			status := "HEALTHY"
			if !result.IsHealthy {
				status = "UNHEALTHY"
			}
			
			fmt.Printf("%s %s\n", status, result.URL)
			fmt.Printf("   Status: %d | Response Time: %v\n", 
				result.StatusCode, result.ResponseTime.Round(time.Millisecond))
			
			if result.Error != "" {
				fmt.Printf("   Error: %s\n", result.Error)
			}
			fmt.Println()
		}
		
		fmt.Println("💤 Waiting 15 seconds...\n")
		time.Sleep(15 * time.Second)
	}
}