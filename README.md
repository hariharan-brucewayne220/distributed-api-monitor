# API Monitor

An HTTP API monitor written in Go. Any number of worker processes split the
checking work through Postgres, a web dashboard reads what they record, and an
optional local OpenAI-compatible LLM (or a rule-based fallback) writes
natural-language "insights" about the current state.

## Features

- **Horizontally scalable workers** - run one `cmd/monitor` process or twenty,
  on one host or several. They lease due endpoints from Postgres, so each
  scheduled check is done by exactly one worker, and when a worker dies its
  endpoints move to the others once its leases expire
- **Concurrent checks** - each worker checks its leased batch in parallel
  (goroutines + `sync.WaitGroup`), batch size `MAX_CONCURRENCY`
- **Shared endpoint registry** - the monitored URL list lives in Postgres, so
  every web replica and worker sees the same list and it survives restarts
- **Web dashboard** - status cards, which worker checked each endpoint, how
  many workers are alive, a response-time chart seeded from history, and
  add/remove endpoints from the page
- **AI insights** - `internal/ai` posts the latest results to
  `{AI_BASE_URL}/v1/chat/completions` and parses a JSON list of insights;
  on any error (or `AI_ENABLED=false`) it falls back to rule-based insights.
  Answers are cached for `INSIGHTS_TTL` so dashboards polling every 5s do not
  cause an LLM call per poll
- **Single-process mode** - without `DATABASE_URL` the same code runs with an
  in-memory coordinator: one process, nothing persisted
- **Docker Compose** - `docker-compose.yml` runs Postgres, three workers and
  the dashboard; `docker-compose.ai.yml` adds a local GGUF model server

## Architecture

```
            +-----------+   +-----------+   +-----------+
            | worker 1  |   | worker 2  |   | worker N  |   cmd/monitor (or the
            | us-east   |   | us-east   |   | eu-west   |   worker embedded in cmd/web)
            +-----+-----+   +-----+-----+   +-----+-----+
     lease due    |               |               |     check targets over HTTP,
     endpoints,   +-------+-------+-------+-------+     record results, heartbeat
     record       |                       |
                  v                       v
            +---------------------------------------+
            | Postgres                              |
            |  endpoints   registry + lease columns |
            |  check_results  history (+ worker_id) |
            |  workers     heartbeats               |
            +---------------------------------------+
                  ^                       ^
     read latest, |                       |
     history,     |                       |
     registry  +--+---------+     +-------+-----+
               | web replica |     | web replica |   cmd/web: dashboard + JSON API,
               +-------------+     +-------------+   never checks on request
```

**How work is split.** Each worker loops: lease up to `MAX_CONCURRENCY`
endpoints that are due and not leased, check them concurrently, record the
results, sleep `POLL_INTERVAL` if nothing was due. The lease is one `UPDATE ...
FROM (SELECT ... FOR UPDATE SKIP LOCKED)`: workers claiming at the same moment
skip each other's rows instead of blocking on them or taking them twice.

**Failure handling.** A lease is a row-level timestamp (`lease_until`), not a
held lock, so it outlives the claiming transaction. If a worker dies mid-check,
the endpoint is skipped by everyone until `LEASE_DURATION` passes, then the next
worker takes it. Every claim bumps a per-endpoint `lease_token`, and a result
is only recorded if its token is still current: a worker that was merely slow
and reports back after its lease was taken over is ignored instead of writing
a duplicate or moving the schedule. All lease and schedule comparisons use the
database's `now()`, so clock skew between worker hosts cannot make two workers
believe they hold the same endpoint.

**Startup.** Schema creation and migration run inside one transaction holding a
Postgres advisory lock, because several workers starting at once would
otherwise race on `CREATE TABLE IF NOT EXISTS` (which fails with a `pg_type`
unique violation under concurrency). The default endpoints are seeded once per
database, so an edited list is never re-seeded on restart.

### What this is not

Being honest about the limits:

- **Postgres is a single point of failure and the only coordination point.**
  If it is down, workers keep retrying and the dashboard returns errors; no
  checks are recorded. High availability means running Postgres with a
  replica/failover setup, which this repo does not provide.
- **Delivery is at-least-once per lease, recorded at-most-once.** A worker that
  stalls past its lease means the endpoint can be *requested* twice in that
  window; only one result is stored.
- **Failover is not instant.** A crashed worker's endpoints wait for
  `LEASE_DURATION` (default `2 x REQUEST_TIMEOUT + 10s`, so 20s).
- **Regions are labels.** `WORKER_REGION` is recorded with heartbeats and shown
  on the dashboard, but there is no region-aware scheduling: any worker may
  check any endpoint, so you cannot yet ask for "check this from both regions".
- **Scale ceiling is the database.** Each round is a few small queries per
  worker; hundreds of workers and tens of thousands of endpoints are fine for
  Postgres, but this has not been load-tested beyond the tests below.

## Layout

```
cmd/web       dashboard + JSON API; runs an embedded worker unless EMBEDDED_WORKER=false
cmd/monitor   standalone check worker; run as many as you like
cmd/query     prints recent stored results for one URL
cmd/mock-ai   fake OpenAI-compatible server for demos (canned answers)
internal/worker    the lease / check / record loop
internal/checker   HTTP checks (Check, CheckMultiple)
internal/storage   Store and Coordinator interfaces, PostgresStore, MemoryStore
internal/ai        OpenAI-compatible client + rule-based FallbackInsights
internal/config    env-based configuration
gguf_server.py     Flask + llama-cpp-python OpenAI-compatible shim (Dockerfile.gguf)
```

## Quick start

### Demo (mock AI, single process, in memory)

```bash
./demo.sh            # starts cmd/mock-ai on :8000 and cmd/web on :8080
```

Open http://localhost:8080.

### Cluster in containers

```bash
docker compose up --build                     # Postgres + 3 workers + dashboard
docker compose up --build --scale worker=6    # more workers
docker compose stop worker                    # ...or stop some and watch the rest take over
```

### Cluster by hand

```bash
docker compose up -d postgres
export DATABASE_URL="host=localhost port=5432 user=monitor password=password dbname=api_monitor sslmode=disable"

go run ./cmd/monitor -id w1 -region us-east &
go run ./cmd/monitor -id w2 -region us-east &
go run ./cmd/monitor -id w3 -region eu-west &
EMBEDDED_WORKER=false go run ./cmd/web                  # replica on :8080
EMBEDDED_WORKER=false WEB_PORT=8081 go run ./cmd/web    # second replica, same data
```

`GET /api/workers` lists the workers; the `checkedBy` field in `/api/status`
and `worker_id` in `check_results` show who did each check.

### Query CLI

```bash
go run ./cmd/query -url https://httpbin.org/status/200 -limit 20
```

### Local GGUF model

```bash
MODEL_DIR=/path/to/gguf-dir docker compose -f docker-compose.ai.yml up   # Postgres + GGUF server + dashboard
# or, without Docker:
./scripts/setup-local-gguf.sh
```

## API

| Route | Description |
| --- | --- |
| `GET /` | Dashboard |
| `GET /api/status` | Latest stored result per registered endpoint, with `checkedBy`. Does not trigger checks |
| `GET /api/history?endpoint=<url>&limit=N` | Most recent stored results for one endpoint, newest first (default 50, max 1000) |
| `GET /api/insights` | Insights for the latest results (AI when configured, rule-based otherwise), cached for `INSIGHTS_TTL` |
| `GET` / `POST` / `DELETE /api/endpoints` | List, add (`{"url": ...}`), or remove a monitored URL; shared by all replicas |
| `GET /api/workers` | Workers seen in the last hour, with region, heartbeat age and `alive` (heartbeat within 30s) |

`responseTime` values are Go durations in nanoseconds. Added URLs must be
`http(s)` and at most 2000 characters. A new endpoint is due immediately, so it
appears in `/api/status` after the next worker round (about `POLL_INTERVAL`).
With `DATABASE_URL` set the registry is stored in Postgres; without it, it
resets to the defaults when the process restarts.

## Configuration

```bash
# Coordination + persistence (unset = single process, in memory)
DATABASE_URL="host=localhost port=5432 user=monitor password=password dbname=api_monitor sslmode=disable"

# Monitoring
CHECK_INTERVAL="15s"      # interval given to newly added / seeded endpoints
REQUEST_TIMEOUT="5s"
MAX_CONCURRENCY=10        # endpoints one worker leases and checks per round
WEB_PORT=8080

# Workers
WORKER_ID=""              # default hostname-pid-random
WORKER_REGION="local"     # label shown on the dashboard
LEASE_DURATION="20s"      # default 2 x REQUEST_TIMEOUT + 10s; keep it above REQUEST_TIMEOUT
POLL_INTERVAL="1s"        # wait after a round that found nothing due
EMBEDDED_WORKER=true      # run a worker inside cmd/web

# AI (any OpenAI-compatible chat/completions server)
AI_ENABLED=true
AI_BASE_URL="http://localhost:8000"
AI_API_KEY="your-api-key"
AI_MODEL="gpt-oss-20b"
INSIGHTS_TTL="30s"
```

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

Tests cover the checker (httptest servers for 200 / 500 / timeout and
concurrency), the AI client (fake OpenAI server, error fallback), the lease
semantics on the in-memory coordinator (exclusive claims, expiry and takeover,
stale-token fencing, removal mid-check, seed-once) with a fake clock, the
worker loop, and every HTTP handler.

The Postgres tests are skipped unless `TEST_DATABASE_URL` is set. They run in
throwaway schemas and open several `PostgresStore`s with separate connection
pools to stand in for separate processes:

- 8 stores starting at once on an empty schema (the advisory-lock race)
- 6 workers claiming 60 endpoints concurrently: each claimed exactly once
- a lease left by a "crashed" worker is skipped until it expires, then taken
  over, and the late result from the old lease is discarded
- `TestWorker_PostgresCluster`: three workers run against real HTTP targets for
  5s, one is stopped halfway. It asserts every worker did work, no endpoint was
  checked twice within one interval, the target's request count equals the
  recorded rows, and every endpoint kept being checked after the stop
- plus the timestamp round-trip, long-URL batch, tie ordering and legacy-schema
  migration tests

```bash
docker run -d --name pg -e POSTGRES_PASSWORD=pw -p 5432:5432 postgres:15
TEST_DATABASE_URL="host=localhost port=5432 user=postgres password=pw dbname=postgres sslmode=disable" \
  go test -race ./...
```

### Schema notes

`check_results.checked_at` is `TIMESTAMPTZ` and `url` is `TEXT`. A naive
`TIMESTAMP` column drops the writer's UTC offset and hands the value back
labelled UTC, shifting every timestamp by the local offset; a bounded
`VARCHAR(n)` lets one over-length URL abort the batch transaction and discard
every other endpoint's result. A table created by an earlier version is altered
to these types on startup, and gains the `worker_id` column.
