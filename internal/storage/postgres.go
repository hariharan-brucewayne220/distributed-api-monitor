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

	// Bring a pre-existing table up to the current column types.
	if err := store.migrateSchema(); err != nil {
		return nil, err
	}

	return store, nil
}

// createTables creates the necessary database tables.
//
// checked_at is TIMESTAMPTZ, not TIMESTAMP: CheckResult.CheckedAt is a
// wall-clock instant in the process's local zone, and a naive TIMESTAMP column
// silently discards the UTC offset on write and hands the value back labelled
// UTC on read, shifting every timestamp by the local offset.
//
// url is TEXT rather than VARCHAR(n) because SaveResults writes a whole batch
// in one transaction: a single over-length URL would abort the transaction and
// discard the results for every other endpoint in that batch.
func (s *PostgresStore) createTables() error {
	query := `
	CREATE TABLE IF NOT EXISTS check_results (
		id SERIAL PRIMARY KEY,
		url TEXT NOT NULL,
		status_code INTEGER,
		response_time_ms INTEGER NOT NULL,
		is_healthy BOOLEAN NOT NULL,
		error_message TEXT,
		checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_check_results_url ON check_results(url);
	CREATE INDEX IF NOT EXISTS idx_check_results_checked_at ON check_results(checked_at);
	`

	_, err := s.db.Exec(query)
	return err
}

// migrateSchema upgrades a check_results table created by an earlier version
// to the column types createTables now produces. It is a no-op once the table
// is current, so it is safe to run on every startup.
func (s *PostgresStore) migrateSchema() error {
	var urlType, checkedAtType sql.NullString
	err := s.db.QueryRow(`
	SELECT
		max(CASE WHEN column_name = 'url' THEN data_type END),
		max(CASE WHEN column_name = 'checked_at' THEN data_type END)
	FROM information_schema.columns
	WHERE table_schema = current_schema() AND table_name = 'check_results'
	`).Scan(&urlType, &checkedAtType)
	if err != nil {
		return err
	}

	if urlType.String == "character varying" {
		if _, err := s.db.Exec(`ALTER TABLE check_results ALTER COLUMN url TYPE TEXT`); err != nil {
			return err
		}
	}

	if checkedAtType.String == "timestamp without time zone" {
		// Rows already in the table were written by a build that dropped the
		// offset, and GetRecentResults has been reading them back as UTC.
		// Interpreting them as UTC keeps existing history reading exactly as it
		// did before the upgrade; only new rows get a true instant.
		if _, err := s.db.Exec(
			`ALTER TABLE check_results ALTER COLUMN checked_at TYPE TIMESTAMPTZ USING checked_at AT TIME ZONE 'UTC'`,
		); err != nil {
			return err
		}
	}

	return nil
}

// Compile-time check that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)

const insertResultSQL = `
	INSERT INTO check_results (url, status_code, response_time_ms, is_healthy, error_message, checked_at)
	VALUES ($1, $2, $3, $4, $5, $6)
	`

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertResult(e execer, result checker.CheckResult) error {
	responseTimeMs := int(result.ResponseTime.Milliseconds())
	var errorMessage *string
	if result.Error != "" {
		errorMessage = &result.Error
	}

	_, err := e.Exec(insertResultSQL,
		result.URL,
		result.StatusCode,
		responseTimeMs,
		result.IsHealthy,
		errorMessage,
		result.CheckedAt,
	)
	return err
}

// SaveResult saves a single check result to the database
func (s *PostgresStore) SaveResult(result checker.CheckResult) error {
	return insertResult(s.db, result)
}

// SaveResults saves a batch of check results in one transaction.
func (s *PostgresStore) SaveResults(results []checker.CheckResult) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, result := range results {
		if err := insertResult(tx, result); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetRecentResults gets recent results for a URL, newest first.
//
// id breaks ties on checked_at: the clock backing CheckedAt is coarse enough
// on some platforms (~15ms on Windows) for two checks to land on the same
// timestamp, and without a tiebreak "newest first" is not deterministic.
func (s *PostgresStore) GetRecentResults(url string, limit int) ([]checker.CheckResult, error) {
	query := `
	SELECT url, status_code, response_time_ms, is_healthy, error_message, checked_at
	FROM check_results
	WHERE url = $1
	ORDER BY checked_at DESC, id DESC
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
