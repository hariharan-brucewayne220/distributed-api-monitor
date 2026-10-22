package storage

import (
	"time"

	"api-monitor/internal/checker"
)

// Compile-time check that PostgresStore satisfies Coordinator.
var _ Coordinator = (*PostgresStore)(nil)

// Every time comparison below uses the database's now(), never the caller's
// clock. Workers on different hosts can disagree about the time; if lease
// expiry were judged on each worker's own clock, a worker running fast could
// treat a live lease as expired and two workers would hold the same endpoint.

// ListEndpoints returns the registered URLs in the order they were added.
func (s *PostgresStore) ListEndpoints() ([]string, error) {
	rows, err := s.db.Query(`SELECT url FROM endpoints ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	urls := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, rows.Err()
}

// AddEndpoint registers url, due immediately.
func (s *PostgresStore) AddEndpoint(url string, interval time.Duration) error {
	res, err := s.db.Exec(
		`INSERT INTO endpoints (url, interval_ms) VALUES ($1, $2) ON CONFLICT (url) DO NOTHING`,
		url, interval.Milliseconds(),
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrEndpointExists
	}
	return nil
}

// RemoveEndpoint unregisters url. A worker holding a lease on it will find
// its lease gone at Complete time and its result is discarded.
func (s *PostgresStore) RemoveEndpoint(url string) error {
	res, err := s.db.Exec(`DELETE FROM endpoints WHERE url = $1`, url)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrEndpointNotFound
	}
	return nil
}

// SeedEndpoints registers urls the first time any process calls it against
// this database. The marker row in monitor_meta is what makes it once-only:
// concurrent callers block on the marker's primary key and only the one whose
// insert lands does the seeding.
func (s *PostgresStore) SeedEndpoints(urls []string, interval time.Duration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO monitor_meta (key) VALUES ('endpoints_seeded') ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return nil
	}

	for _, u := range urls {
		if _, err := tx.Exec(
			`INSERT INTO endpoints (url, interval_ms) VALUES ($1, $2) ON CONFLICT (url) DO NOTHING`,
			u, interval.Milliseconds(),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClaimDue leases up to max due endpoints to owner.
//
// FOR UPDATE SKIP LOCKED is what lets many workers run this at once without
// blocking each other or claiming the same row: each one locks the due rows it
// selects, and rows another worker has locked are skipped rather than waited
// on. The lease itself outlives the transaction, so a worker that dies mid
// check only delays that endpoint until lease_until passes.
func (s *PostgresStore) ClaimDue(owner string, max int, lease time.Duration) ([]Lease, error) {
	rows, err := s.db.Query(`
	UPDATE endpoints e
	SET lease_owner = $1,
		lease_until = now() + $2 * interval '1 millisecond',
		lease_token = e.lease_token + 1
	FROM (
		SELECT id FROM endpoints
		WHERE next_check_at <= now()
		  AND (lease_until IS NULL OR lease_until < now())
		ORDER BY next_check_at, id
		LIMIT $3
		FOR UPDATE SKIP LOCKED
	) due
	WHERE e.id = due.id
	RETURNING e.url, e.lease_token
	`, owner, lease.Milliseconds(), max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var leases []Lease
	for rows.Next() {
		var l Lease
		if err := rows.Scan(&l.URL, &l.Token); err != nil {
			return nil, err
		}
		leases = append(leases, l)
	}
	return leases, rows.Err()
}

// Complete records each result whose lease token is still current and pushes
// that endpoint's next check out by its interval, in one transaction.
func (s *PostgresStore) Complete(owner string, done []Completed) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	recorded := 0
	for _, d := range done {
		res, err := tx.Exec(`
		UPDATE endpoints
		SET next_check_at = now() + interval_ms * interval '1 millisecond',
			lease_owner = NULL,
			lease_until = NULL
		WHERE url = $1 AND lease_token = $2
		`, d.Lease.URL, d.Lease.Token)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n == 0 {
			// Re-claimed by another worker, or removed, while we were checking.
			continue
		}

		r := d.Result
		r.WorkerID = owner
		if err := insertResult(tx, r); err != nil {
			return 0, err
		}
		recorded++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return recorded, nil
}

// Heartbeat upserts the worker's row with the database's current time.
func (s *PostgresStore) Heartbeat(w WorkerInfo) error {
	started := w.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	_, err := s.db.Exec(`
	INSERT INTO workers (id, region, started_at, last_heartbeat)
	VALUES ($1, $2, $3, now())
	ON CONFLICT (id) DO UPDATE SET region = EXCLUDED.region, last_heartbeat = now()
	`, w.ID, w.Region, started)
	return err
}

// ListWorkers returns workers seen within workerRetention, by ID.
func (s *PostgresStore) ListWorkers() ([]WorkerInfo, error) {
	rows, err := s.db.Query(`
	SELECT id, region, started_at, last_heartbeat,
		EXTRACT(EPOCH FROM (now() - last_heartbeat))::float8
	FROM workers
	WHERE last_heartbeat > now() - $1 * interval '1 millisecond'
	ORDER BY id
	`, workerRetention.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []WorkerInfo
	for rows.Next() {
		var w WorkerInfo
		var sinceSeconds float64
		if err := rows.Scan(&w.ID, &w.Region, &w.StartedAt, &w.LastHeartbeat, &sinceSeconds); err != nil {
			return nil, err
		}
		w.SinceHeartbeat = time.Duration(sinceSeconds * float64(time.Second))
		workers = append(workers, w)
	}
	return workers, rows.Err()
}

// LatestResults returns the newest result per registered endpoint, in
// registry order.
func (s *PostgresStore) LatestResults() ([]checker.CheckResult, error) {
	rows, err := s.db.Query(`
	SELECT r.url, r.status_code, r.response_time_ms, r.is_healthy, r.error_message, r.checked_at, r.worker_id
	FROM endpoints e
	JOIN LATERAL (
		SELECT * FROM check_results c
		WHERE c.url = e.url
		ORDER BY c.checked_at DESC, c.id DESC
		LIMIT 1
	) r ON true
	ORDER BY e.id
	`)
	if err != nil {
		return nil, err
	}
	return scanResults(rows)
}
