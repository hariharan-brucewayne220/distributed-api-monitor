package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"api-monitor/internal/checker"
	"api-monitor/internal/config"
	"api-monitor/internal/storage"
	"api-monitor/internal/worker"
)

// monitor is a check worker. Run as many copies as you like against the same
// DATABASE_URL (on one host or many): they lease due endpoints from Postgres,
// so each check is done by one worker, and a crashed worker's endpoints are
// picked up by the others once its leases expire.
//
// Without DATABASE_URL (and without -db) it runs alone with an in-memory
// registry of the default endpoints and only prints results.
func main() {
	useDB := flag.Bool("db", false, "Use Postgres (DATABASE_URL, or the docker-compose default)")
	id := flag.String("id", "", "Worker ID (default WORKER_ID, else hostname-pid-random)")
	region := flag.String("region", "", "Region label recorded with each heartbeat (default WORKER_REGION)")
	quiet := flag.Bool("quiet", false, "Do not print each result")
	flag.Parse()

	cfg := config.Load()
	if *id != "" {
		cfg.WorkerID = *id
	}
	if *region != "" {
		cfg.WorkerRegion = *region
	}

	var coord storage.Coordinator
	if *useDB || cfg.DatabaseURL != "" {
		dsn := cfg.DatabaseURL
		if dsn == "" {
			dsn = config.DefaultLocalDatabaseURL
		}
		pg, err := storage.NewPostgresStore(dsn)
		if err != nil {
			log.Fatalf("Failed to connect to database: %v", err)
		}
		coord = pg
		fmt.Println("Database connected: sharing work with every other worker on this database")
	} else {
		coord = storage.NewMemoryStore(storage.DefaultMemoryCapacity)
		fmt.Println("DATABASE_URL not set and -db not given: running alone, results are printed only")
	}
	defer coord.Close()

	if err := coord.SeedEndpoints(config.DefaultEndpoints(), cfg.CheckInterval); err != nil {
		log.Fatalf("Failed to seed endpoints: %v", err)
	}

	w := &worker.Worker{
		ID:            cfg.WorkerID,
		Region:        cfg.WorkerRegion,
		Coord:         coord,
		Checker:       checker.NewHTTPChecker(cfg.RequestTimeout),
		BatchSize:     cfg.MaxConcurrency,
		LeaseDuration: cfg.LeaseDuration,
		PollInterval:  cfg.PollInterval,
	}
	if !*quiet {
		w.OnResults = printResults
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	w.Run(ctx)
}

func printResults(results []checker.CheckResult) {
	fmt.Printf("=== %s: %d checks ===\n", time.Now().Format("15:04:05"), len(results))
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
	}
	fmt.Println()
}
