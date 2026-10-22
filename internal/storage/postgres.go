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
	if err := store.setupSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// schemaLockKey is the advisory lock that serializes schema setup.
const schemaLockKey = 7_219_450_116

// setupSchema creates and migrates the tables in one transaction holding an
// advisory lock. Several workers starting at the same moment would otherwise
// race on CREATE TABLE IF NOT EXISTS, which is not safe under concurrency in
// Postgres (the loser fails on a pg_type unique violation).
func (s *PostgresStore) setupSchema() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, schemaLockKey); err != nil {
		return err
	}
	if err := createTables(tx); err != nil {
		return err
	}
	// Bring a pre-existing table up to the current column types.
	if err := migrateSchema(tx); err != nil {
		return err
	}
	return tx.Commit()
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
//
// endpoints is the shared registry and work queue: next_check_at says when an
// endpoint is due, lease_owner/lease_until say which worker holds it, and
// lease_token is the fencing token that Complete checks.
func createTables(tx *sql.Tx) error {
	query := `
	CREATE TABLE IF NOT EXISTS check_results (
		id SERIAL PRIMARY KEY,
		url TEXT NOT NULL,
		status_code INTEGER,
		response_time_ms INTEGER NOT NULL,
		is_healthy BOOLEAN NOT NULL,
		error_message TEXT,
		checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		worker_id TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_check_results_url ON check_results(url);
	CREATE INDEX IF NOT EXISTS idx_check_results_checked_at ON check_results(checked_at);

	CREATE TABLE IF NOT EXISTS endpoints (
		id BIGSERIAL PRIMARY KEY,
		url TEXT NOT NULL UNIQUE,
		interval_ms BIGINT NOT NULL,
		next_check_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		lease_owner TEXT,
		lease_until TIMESTAMPTZ,
		lease_token BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_endpoints_next_check_at ON endpoints(next_check_at);

	CREATE TABLE IF NOT EXISTS workers (
		id TEXT PRIMARY KEY,
		region TEXT NOT NULL DEFAULT '',
		started_at TIMESTAMPTZ NOT NULL,
		last_heartbeat TIMESTAMPTZ NOT NULL
	);

	CREATE TABLE IF NOT EXISTS monitor_meta (
		key TEXT PRIMARY KEY,
		set_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	`

	_, err := tx.Exec(query)
	return err
}

// migrateSchema upgrades a check_results table created by an earlier version
// to the column types createTables now produces. It is a no-op once the table
// is current, so it is safe to run on every startup.
func migrateSchema(tx *sql.Tx) error {
	var urlType, checkedAtType sql.NullString
	err := tx.QueryRow(`
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
		if _, err := tx.Exec(`ALTER TABLE check_results ALTER COLUMN url TYPE TEXT`); err != nil {
			return err
		}
	}

	if checkedAtType.String == "timestamp without time zone" {
		// Rows already in the table were written by a build that dropped the
		// offset, and GetRecentResults has been reading them back as UTC.
		// Interpreting them as UTC keeps existing history reading exactly as it
		// did before the upgrade; only new rows get a true instant.
		if _, err := tx.Exec(
			`ALTER TABLE check_results ALTER COLUMN checked_at TYPE TIMESTAMPTZ USING checked_at AT TIME ZONE 'UTC'`,
		); err != nil {
			return err
		}
	}

	_, err = tx.Exec(`ALTER TABLE check_results ADD COLUMN IF NOT EXISTS worker_id TEXT`)
	return err
}

// Compile-time check that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)

const insertResultSQL = `
	INSERT INTO check_results (url, status_code, response_time_ms, is_healthy, error_message, checked_at, worker_id)
	VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertResult(e execer, result checker.CheckResult) error {
	responseTimeMs := int(result.ResponseTime.Milliseconds())
	var errorMessage, workerID *string
	if result.Error != "" {
		errorMessage = &result.Error
	}
	if result.WorkerID != "" {
		workerID = &result.WorkerID
	}

	_, err := e.Exec(insertResultSQL,
		result.URL,
		result.StatusCode,
		responseTimeMs,
		result.IsHealthy,
		errorMessage,
		result.CheckedAt,
		workerID,
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
	SELECT ` + resultColumns + `
	FROM check_results
	WHERE url = $1
	ORDER BY checked_at DESC, id DESC
	LIMIT $2
	`

	rows, err := s.db.Query(query, url, limit)
	if err != nil {
		return nil, err
	}
	return scanResults(rows)
}

const resultColumns = `url, status_code, response_time_ms, is_healthy, error_message, checked_at, worker_id`

// scanResults reads rows selected with resultColumns and closes them.
func scanResults(rows *sql.Rows) ([]checker.CheckResult, error) {
	defer rows.Close()

	var results []checker.CheckResult
	for rows.Next() {
		var result checker.CheckResult
		var responseTimeMs int
		var errorMessage, workerID sql.NullString

		err := rows.Scan(
			&result.URL,
			&result.StatusCode,
			&responseTimeMs,
			&result.IsHealthy,
			&errorMessage,
			&result.CheckedAt,
			&workerID,
		)
		if err != nil {
			return nil, err
		}

		result.ResponseTime = time.Duration(responseTimeMs) * time.Millisecond
		result.Error = errorMessage.String
		result.WorkerID = workerID.String

		results = append(results, result)
	}

	return results, rows.Err()
}

// Close closes the database connection
func (s *PostgresStore) Close() error {
	return s.db.Close()
}
