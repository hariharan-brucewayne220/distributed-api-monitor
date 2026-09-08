package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"api-monitor/internal/checker"
	"api-monitor/internal/config"
	"api-monitor/internal/storage"
)

// monitor is the headless polling loop: it checks the default endpoints every
// CHECK_INTERVAL and prints the results. Pass -db (or set DATABASE_URL) to
// also write each batch to Postgres, using the same storage package as the
// web server.
func main() {
	useDB := flag.Bool("db", false, "Persist results to Postgres (DATABASE_URL, or the docker-compose default)")
	flag.Parse()

	cfg := config.Load()

	fmt.Println("Starting API Monitor...")

	httpChecker := checker.NewHTTPChecker(cfg.RequestTimeout)

	var store storage.Store
	if *useDB || cfg.DatabaseURL != "" {
		dsn := cfg.DatabaseURL
		if dsn == "" {
			dsn = config.DefaultLocalDatabaseURL
		}
		fmt.Println("Connecting to database...")
		pg, err := storage.NewPostgresStore(dsn)
		if err != nil {
			log.Fatalf("Failed to connect to database: %v", err)
		}
		defer pg.Close()
		store = pg
		fmt.Println("Database connected; results will be persisted")
	} else {
		fmt.Println("DATABASE_URL not set and -db not given: results are printed only")
	}

	urls := config.DefaultEndpoints()
	fmt.Printf("Monitoring %d endpoints every %v\n\n", len(urls), cfg.CheckInterval)

	for {
		results := httpChecker.CheckMultiple(urls)

		if store != nil {
			if err := store.SaveResults(results); err != nil {
				log.Printf("Failed to save results: %v", err)
			} else {
				fmt.Println("Results saved to database")
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

		fmt.Printf("Waiting %v...\n\n", cfg.CheckInterval)
		time.Sleep(cfg.CheckInterval)
	}
}
