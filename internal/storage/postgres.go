package storage

import (
	"database/sql"
	"time"

	"api-monitor/internal/checker"
	_ "github.com/lib/pq"
)

// PostgresStore handles database operations
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore creates a new PostgreSQL storage
func NewPostgresStore(connectionString string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		return nil, err
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, err
	}

	store := &PostgresStore{db: db}
	
	// Create tables if they don't exist
	if err := store.createTables(); err != nil {
		return nil, err
	}

	return store, nil
}

// createTables creates the necessary database tables
func (s *PostgresStore) createTables() error {
	query := `
	CREATE TABLE IF NOT EXISTS check_results (
		id SERIAL PRIMARY KEY,
		url VARCHAR(500) NOT NULL,
		status_code INTEGER,
		response_time_ms INTEGER NOT NULL,
		is_healthy BOOLEAN NOT NULL,
		error_message TEXT,
		checked_at TIMESTAMP NOT NULL DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_check_results_url ON check_results(url);
	CREATE INDEX IF NOT EXISTS idx_check_results_checked_at ON check_results(checked_at);
	`
	
	_, err := s.db.Exec(query)
	return err
}

// SaveResult saves a check result to the database
func (s *PostgresStore) SaveResult(result checker.CheckResult) error {
	query := `
	INSERT INTO check_results (url, status_code, response_time_ms, is_healthy, error_message, checked_at)
	VALUES ($1, $2, $3, $4, $5, $6)
	`
	
	responseTimeMs := int(result.ResponseTime.Milliseconds())
	var errorMessage *string
	if result.Error != "" {
		errorMessage = &result.Error
	}
	
	_, err := s.db.Exec(query, 
		result.URL, 
		result.StatusCode, 
		responseTimeMs, 
		result.IsHealthy, 
		errorMessage, 
		result.CheckedAt,
	)
	
	return err
}

// SaveResults saves multiple check results
func (s *PostgresStore) SaveResults(results []checker.CheckResult) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, result := range results {
		if err := s.SaveResult(result); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetRecentResults gets recent results for a URL
func (s *PostgresStore) GetRecentResults(url string, limit int) ([]checker.CheckResult, error) {
	query := `
	SELECT url, status_code, response_time_ms, is_healthy, error_message, checked_at
	FROM check_results 
	WHERE url = $1 
	ORDER BY checked_at DESC 
	LIMIT $2
	`
	
	rows, err := s.db.Query(query, url, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []checker.CheckResult
	for rows.Next() {
		var result checker.CheckResult
		var responseTimeMs int
		var errorMessage sql.NullString
		
		err := rows.Scan(
			&result.URL,
			&result.StatusCode,
			&responseTimeMs,
			&result.IsHealthy,
			&errorMessage,
			&result.CheckedAt,
		)
		if err != nil {
			return nil, err
		}
		
		result.ResponseTime = time.Duration(responseTimeMs) * time.Millisecond
		if errorMessage.Valid {
			result.Error = errorMessage.String
		}
		
		results = append(results, result)
	}
	
	return results, rows.Err()
}

// Close closes the database connection
func (s *PostgresStore) Close() error {
	return s.db.Close()
}