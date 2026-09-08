package storage

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"api-monitor/internal/checker"
)

// These tests exercise PostgresStore against a real database, which is the
// only way to catch the things a fake Store cannot model: column types, the
// batch transaction, and how lib/pq round-trips a time.Time.
//
// They are skipped unless TEST_DATABASE_URL points at a scratch database:
//
//	docker run -d --name pg -e POSTGRES_PASSWORD=pw -p 5432:5432 postgres:15
//	TEST_DATABASE_URL="host=localhost port=5432 user=postgres password=pw dbname=postgres sslmode=disable" \
//	  go test ./internal/storage -run Postgres -v

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live Postgres tests")
	}
	return dsn
}

// createTestSchema makes a throwaway schema and registers its removal.
//
// The drop opens its own connection: cleanups registered with t.Cleanup run
// after the test function returns, by which point any connection the test
// closed with defer is already gone and the DROP would be silently skipped.
func createTestSchema(t *testing.T, dsn string) string {
	t.Helper()
	schema := fmt.Sprintf("api_monitor_test_%d", time.Now().UnixNano())

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	t.Cleanup(func() {
		cleanup, err := sql.Open("postgres", dsn)
		if err != nil {
			t.Errorf("reconnect to drop schema %s: %v", schema, err)
			return
		}
		defer cleanup.Close()
		if _, err := cleanup.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("drop test schema %s: %v", schema, err)
		}
	})

	return schema
}

// newIsolatedStore opens a PostgresStore whose check_results table lives in a
// throwaway schema, so tests neither see each other's rows nor touch real data.
func newIsolatedStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := testDSN(t)
	schema := createTestSchema(t, dsn)

	store, err := NewPostgresStore(dsn + " search_path=" + schema)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	return store
}

// TestPostgresStore_RoundTripPreservesInstant is the regression test for a
// naive TIMESTAMP column: it dropped the UTC offset on write and relabelled the
// value UTC on read, so every timestamp came back shifted by the local offset.
func TestPostgresStore_RoundTripPreservesInstant(t *testing.T) {
	store := newIsolatedStore(t)

	url := fmt.Sprintf("http://example.test/instant/%d", time.Now().UnixNano())
	// Microseconds: that is the precision Postgres keeps.
	want := time.Now().Truncate(time.Microsecond)

	if err := store.SaveResults([]checker.CheckResult{{
		URL:          url,
		StatusCode:   200,
		ResponseTime: 123 * time.Millisecond,
		IsHealthy:    true,
		CheckedAt:    want,
	}}); err != nil {
		t.Fatalf("SaveResults: %v", err)
	}

	got, err := store.GetRecentResults(url, 10)
	if err != nil {
		t.Fatalf("GetRecentResults: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}

	if !got[0].CheckedAt.Equal(want) {
		t.Errorf("CheckedAt round-trip changed the instant:\n got %s (UTC %s)\nwant %s (UTC %s)\n skew %v",
			got[0].CheckedAt, got[0].CheckedAt.UTC(), want, want.UTC(), got[0].CheckedAt.Sub(want))
	}
	if got[0].ResponseTime != 123*time.Millisecond {
		t.Errorf("ResponseTime = %v, want 123ms", got[0].ResponseTime)
	}
	if !got[0].IsHealthy || got[0].StatusCode != 200 {
		t.Errorf("healthy/status round-trip wrong: %+v", got[0])
	}
}

// TestPostgresStore_LongURLDoesNotDiscardBatch is the regression test for
// url VARCHAR(500): SaveResults writes the batch in one transaction, so a URL
// past the column width used to roll back every other endpoint's result too.
func TestPostgresStore_LongURLDoesNotDiscardBatch(t *testing.T) {
	store := newIsolatedStore(t)

	stamp := time.Now().UnixNano()
	shortURL := fmt.Sprintf("http://example.test/short/%d", stamp)
	longURL := fmt.Sprintf("http://example.test/long/%d?pad=%s", stamp, repeat("z", 1200))
	if len(longURL) <= 500 {
		t.Fatalf("test URL should exceed the old 500-char column, got %d", len(longURL))
	}
	if len(longURL) > MaxURLLength {
		t.Fatalf("test URL should stay within MaxURLLength, got %d", len(longURL))
	}

	now := time.Now().Truncate(time.Microsecond)
	err := store.SaveResults([]checker.CheckResult{
		{URL: shortURL, StatusCode: 200, IsHealthy: true, CheckedAt: now},
		{URL: longURL, StatusCode: 200, IsHealthy: true, CheckedAt: now},
	})
	if err != nil {
		t.Fatalf("SaveResults with a long URL: %v", err)
	}

	for _, url := range []string{shortURL, longURL} {
		got, err := store.GetRecentResults(url, 10)
		if err != nil {
			t.Fatalf("GetRecentResults(%d-char url): %v", len(url), err)
		}
		if len(got) != 1 {
			t.Errorf("got %d results for the %d-char URL, want 1", len(got), len(url))
		}
	}
}

// TestPostgresStore_NewestFirstWithTiedTimestamps pins the ordering when the
// clock backing CheckedAt is too coarse to separate two checks.
func TestPostgresStore_NewestFirstWithTiedTimestamps(t *testing.T) {
	store := newIsolatedStore(t)

	url := fmt.Sprintf("http://example.test/tie/%d", time.Now().UnixNano())
	tied := time.Now().Truncate(time.Microsecond)

	// Same timestamp, distinguishable payloads, saved oldest first.
	for i := 1; i <= 3; i++ {
		if err := store.SaveResults([]checker.CheckResult{{
			URL:          url,
			StatusCode:   200,
			ResponseTime: time.Duration(i) * time.Millisecond,
			IsHealthy:    true,
			CheckedAt:    tied,
		}}); err != nil {
			t.Fatalf("SaveResults #%d: %v", i, err)
		}
	}

	got, err := store.GetRecentResults(url, 10)
	if err != nil {
		t.Fatalf("GetRecentResults: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}

	want := []time.Duration{3 * time.Millisecond, 2 * time.Millisecond, 1 * time.Millisecond}
	for i, w := range want {
		if got[i].ResponseTime != w {
			t.Errorf("result[%d].ResponseTime = %v, want %v (newest first must be deterministic under tied timestamps)",
				i, got[i].ResponseTime, w)
		}
	}
}

// TestPostgresStore_MigratesLegacySchema proves an existing table written by
// the previous schema is upgraded in place rather than left on the old types.
func TestPostgresStore_MigratesLegacySchema(t *testing.T) {
	dsn := testDSN(t)

	schema := createTestSchema(t, dsn)

	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	// The schema this project shipped before the fix.
	if _, err := admin.Exec(fmt.Sprintf(`
		CREATE TABLE %s.check_results (
			id SERIAL PRIMARY KEY,
			url VARCHAR(500) NOT NULL,
			status_code INTEGER,
			response_time_ms INTEGER NOT NULL,
			is_healthy BOOLEAN NOT NULL,
			error_message TEXT,
			checked_at TIMESTAMP NOT NULL DEFAULT NOW()
		)`, schema)); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}

	store, err := NewPostgresStore(dsn + " search_path=" + schema)
	if err != nil {
		t.Fatalf("NewPostgresStore against legacy table: %v", err)
	}
	defer store.Close()

	types := map[string]string{}
	rows, err := admin.Query(`
		SELECT column_name, data_type FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'check_results'`, schema)
	if err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, dataType string
		if err := rows.Scan(&name, &dataType); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		types[name] = dataType
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}

	if got := types["url"]; got != "text" {
		t.Errorf("url column = %q, want %q after migration", got, "text")
	}
	if got := types["checked_at"]; got != "timestamp with time zone" {
		t.Errorf("checked_at column = %q, want %q after migration", got, "timestamp with time zone")
	}

	// And the migrated table is usable: a long URL and an exact instant.
	url := fmt.Sprintf("http://example.test/migrated/%s", repeat("y", 900))
	want := time.Now().Truncate(time.Microsecond)
	if err := store.SaveResults([]checker.CheckResult{{
		URL: url, StatusCode: 200, IsHealthy: true, CheckedAt: want,
	}}); err != nil {
		t.Fatalf("SaveResults after migration: %v", err)
	}
	got, err := store.GetRecentResults(url, 5)
	if err != nil {
		t.Fatalf("GetRecentResults after migration: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results after migration, want 1", len(got))
	}
	if !got[0].CheckedAt.Equal(want) {
		t.Errorf("CheckedAt after migration = %s, want %s", got[0].CheckedAt, want)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
