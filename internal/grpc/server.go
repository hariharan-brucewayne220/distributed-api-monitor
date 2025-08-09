package grpc

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"api-monitor/internal/checker"
	"api-monitor/internal/storage"

	"google.golang.org/grpc"
)

// MonitorEndpoint represents a monitored endpoint
type MonitorEndpoint struct {
	ID              string
	URL             string
	IntervalSeconds int32
	TimeoutSeconds  int32
	Enabled         bool
}

// MonitorServer implements our monitoring gRPC service
type MonitorServer struct {
	store           *storage.PostgresStore
	endpoints       map[string]*MonitorEndpoint
	endpointsMutex  sync.RWMutex
	checker         *checker.HTTPChecker
	stopChannels    map[string]chan bool
	resultStream    chan *checker.CheckResult
}

// NewMonitorServer creates a new gRPC monitor server
func NewMonitorServer(store *storage.PostgresStore) *MonitorServer {
	return &MonitorServer{
		store:        store,
		endpoints:    make(map[string]*MonitorEndpoint),
		checker:      checker.NewHTTPChecker(10 * time.Second),
		stopChannels: make(map[string]chan bool),
		resultStream: make(chan *checker.CheckResult, 100),
	}
}

// AddEndpoint adds a new endpoint to monitor
func (s *MonitorServer) AddEndpoint(ctx context.Context, url string, intervalSec, timeoutSec int32) (string, error) {
	s.endpointsMutex.Lock()
	defer s.endpointsMutex.Unlock()

	endpointID := fmt.Sprintf("endpoint_%d", time.Now().Unix())
	
	endpoint := &MonitorEndpoint{
		ID:              endpointID,
		URL:             url,
		IntervalSeconds: intervalSec,
		TimeoutSeconds:  timeoutSec,
		Enabled:         true,
	}

	s.endpoints[endpointID] = endpoint
	
	// Start monitoring this endpoint
	s.startMonitoring(endpoint)
	
	log.Printf("Added endpoint: %s (%s)", endpointID, url)
	return endpointID, nil
}

// ListEndpoints returns all monitored endpoints
func (s *MonitorServer) ListEndpoints() []*MonitorEndpoint {
	s.endpointsMutex.RLock()
	defer s.endpointsMutex.RUnlock()

	endpoints := make([]*MonitorEndpoint, 0, len(s.endpoints))
	for _, endpoint := range s.endpoints {
		endpoints = append(endpoints, endpoint)
	}
	
	return endpoints
}

// GetResults gets recent results for a URL
func (s *MonitorServer) GetResults(url string, limit int) ([]checker.CheckResult, error) {
	if s.store != nil {
		return s.store.GetRecentResults(url, limit)
	}
	return []checker.CheckResult{}, nil
}

// startMonitoring starts monitoring an endpoint in a separate goroutine
func (s *MonitorServer) startMonitoring(endpoint *MonitorEndpoint) {
	stopChan := make(chan bool, 1)
	s.stopChannels[endpoint.ID] = stopChan

	go func() {
		ticker := time.NewTicker(time.Duration(endpoint.IntervalSeconds) * time.Second)
		defer ticker.Stop()

		// Create checker with endpoint-specific timeout
		endpointChecker := checker.NewHTTPChecker(time.Duration(endpoint.TimeoutSeconds) * time.Second)

		for {
			select {
			case <-ticker.C:
				if endpoint.Enabled {
					result := endpointChecker.Check(endpoint.URL)
					
					// Save to database
					if s.store != nil {
						if err := s.store.SaveResult(result); err != nil {
							log.Printf("Failed to save result for %s: %v", endpoint.URL, err)
						}
					}

					// Send to stream
					select {
					case s.resultStream <- &result:
					default:
						// Channel full, skip this result
					}

					// Log the result
					status := "✅"
					if !result.IsHealthy {
						status = "❌"
					}
					log.Printf("%s %s - %v", status, endpoint.URL, result.ResponseTime.Round(time.Millisecond))
				}

			case <-stopChan:
				log.Printf("Stopped monitoring %s", endpoint.URL)
				return
			}
		}
	}()
}

// StopMonitoring stops monitoring an endpoint
func (s *MonitorServer) StopMonitoring(endpointID string) {
	s.endpointsMutex.Lock()
	defer s.endpointsMutex.Unlock()

	if stopChan, exists := s.stopChannels[endpointID]; exists {
		close(stopChan)
		delete(s.stopChannels, endpointID)
		delete(s.endpoints, endpointID)
	}
}

// GetResultStream returns the channel for streaming results
func (s *MonitorServer) GetResultStream() <-chan *checker.CheckResult {
	return s.resultStream
}

// StartGRPCServer starts the gRPC server
func (s *MonitorServer) StartGRPCServer(port int) error {
	listen, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}

	server := grpc.NewServer()
	
	log.Printf("🚀 gRPC server starting on port %d", port)
	return server.Serve(listen)
}