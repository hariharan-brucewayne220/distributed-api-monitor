// Package worker runs the check loop that any number of processes can run at
// once against a shared storage.Coordinator. Each round a worker leases the
// endpoints that are due, checks them concurrently, and hands the results
// back; the coordinator makes sure no two live workers hold the same endpoint
// and that a worker whose lease ran out cannot record over a newer claim.
package worker

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"api-monitor/internal/checker"
	"api-monitor/internal/storage"
)

// Worker leases due endpoints from a Coordinator and checks them.
type Worker struct {
	ID     string
	Region string

	Coord   storage.Coordinator
	Checker *checker.HTTPChecker

	// BatchSize caps how many endpoints one round leases, and so how many
	// checks run concurrently in this process.
	BatchSize int
	// LeaseDuration must comfortably exceed the checker's timeout: a lease
	// that expires while its check is still running lets another worker
	// re-check the same endpoint.
	LeaseDuration time.Duration
	// PollInterval is how long to wait after a round that found nothing due.
	PollInterval time.Duration
	// HeartbeatInterval throttles heartbeat writes.
	HeartbeatInterval time.Duration

	// OnResults, if set, receives each round's check results.
	OnResults func([]checker.CheckResult)

	startedAt     time.Time
	lastHeartbeat time.Time
}

// DefaultID returns an identifier that is unique per process: hostname, pid
// and a random suffix (pids repeat across containers).
func DefaultID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d-%04x", host, os.Getpid(), rand.Intn(1<<16))
}

func (w *Worker) defaults() {
	if w.ID == "" {
		w.ID = DefaultID()
	}
	if w.BatchSize <= 0 {
		w.BatchSize = 10
	}
	if w.LeaseDuration <= 0 {
		w.LeaseDuration = 30 * time.Second
	}
	if w.PollInterval <= 0 {
		w.PollInterval = time.Second
	}
	if w.HeartbeatInterval <= 0 {
		w.HeartbeatInterval = 5 * time.Second
	}
	if w.startedAt.IsZero() {
		w.startedAt = time.Now()
	}
}

// RunOnce performs one round: heartbeat, lease, check, complete. It returns
// how many endpoints were leased and how many results were recorded (fewer
// when a lease was lost or an endpoint was removed mid-check).
func (w *Worker) RunOnce() (claimed, recorded int, err error) {
	w.defaults()

	if time.Since(w.lastHeartbeat) >= w.HeartbeatInterval {
		if err := w.Coord.Heartbeat(storage.WorkerInfo{ID: w.ID, Region: w.Region, StartedAt: w.startedAt}); err != nil {
			return 0, 0, fmt.Errorf("heartbeat: %w", err)
		}
		w.lastHeartbeat = time.Now()
	}

	leases, err := w.Coord.ClaimDue(w.ID, w.BatchSize, w.LeaseDuration)
	if err != nil {
		return 0, 0, fmt.Errorf("claim: %w", err)
	}
	if len(leases) == 0 {
		return 0, 0, nil
	}

	urls := make([]string, len(leases))
	for i, l := range leases {
		urls[i] = l.URL
	}
	results := w.Checker.CheckMultiple(urls)

	done := make([]storage.Completed, len(leases))
	for i := range leases {
		results[i].WorkerID = w.ID
		done[i] = storage.Completed{Lease: leases[i], Result: results[i]}
	}
	recorded, err = w.Coord.Complete(w.ID, done)
	if err != nil {
		return len(leases), 0, fmt.Errorf("complete: %w", err)
	}
	if recorded < len(leases) {
		log.Printf("worker %s: %d of %d results discarded (lease lost or endpoint removed)", w.ID, len(leases)-recorded, len(leases))
	}
	if w.OnResults != nil {
		w.OnResults(results)
	}
	return len(leases), recorded, nil
}

// Run loops RunOnce until ctx is cancelled. A full batch means more work may
// be waiting, so the next round starts at once; otherwise it sleeps
// PollInterval. Coordinator errors (a database restart, say) are logged and
// retried rather than ending the loop.
func (w *Worker) Run(ctx context.Context) {
	w.defaults()
	log.Printf("worker %s (region %q) started: batch %d, lease %v", w.ID, w.Region, w.BatchSize, w.LeaseDuration)
	for {
		claimed, _, err := w.RunOnce()
		if err != nil {
			log.Printf("worker %s: %v", w.ID, err)
		}

		wait := w.PollInterval
		if err == nil && claimed == w.BatchSize {
			wait = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
