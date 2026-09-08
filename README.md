# API Monitor

A concurrent HTTP API monitor written in Go, with a web dashboard, optional
Postgres history, and natural-language "insights" from a local
OpenAI-compatible LLM (or a rule-based fallback when no model is running).

## Features

- **Concurrent checks** - every monitored URL is checked in parallel
  (goroutines + `sync.WaitGroup`) with a configurable timeout
- **Web dashboard** - live status cards, a per-endpoint response-time chart
  seeded from history, add/remove endpoints from the page
- **Optional Postgres history** - set `DATABASE_URL` and every check batch is
  written to a `check_results` table; `GET /api/history` reads it back.
  Without `DATABASE_URL`, results are kept in memory for the life of the
  process and a log line says persistence is off
- **AI insights** - `internal/ai` posts the current results to
  `{AI_BASE_URL}/v1/chat/completions` and parses a JSON list of insights;
  on any error (or `AI_ENABLED=false`) it falls back to rule-based insights
- **Docker Compose** - `docker-compose.yml` runs Postgres + the dashboard with
  persistence on; `docker-compose.ai.yml` adds a local GGUF model server

## Layout

```
cmd/web       dashboard + JSON API (the main binary)
cmd/monitor   headless polling loop that prints results (and persists with -db)
cmd/query     prints recent stored results for one URL
cmd/mock-ai   fake OpenAI-compatible server for demos
internal/checker   HTTP checks (Check, CheckMultiple)
internal/storage   Store interface, PostgresStore, MemoryStore
internal/ai        OpenAI-compatible client + rule-based FallbackInsights
internal/config    env-based configuration
gguf_server.py     Flask + llama-cpp-python OpenAI-compatible shim (Dockerfile.gguf)
```

## Quick start

### Demo (mock AI, in-memory history)

```bash
./demo.sh            # starts cmd/mock-ai on :8000 and cmd/web on :8080
```

Open http://localhost:8080.

### Dashboard with Postgres history

```bash
docker-compose up -d postgres
export DATABASE_URL="host=localhost port=5432 user=monitor password=password dbname=api_monitor sslmode=disable"
go run ./cmd/web
```

Or run everything in containers (persistence is on by default):

```bash
docker-compose up --build
```

### Headless monitor and query CLIs

```bash
go run ./cmd/monitor            # print checks every CHECK_INTERVAL
go run ./cmd/monitor -db        # also persist (DATABASE_URL or compose defaults)
go run ./cmd/query -url https://httpbin.org/status/200 -limit 20
```

### Local GGUF model

```bash
docker-compose -f docker-compose.ai.yml up      # Postgres + GGUF server + dashboard
# or, without Docker:
./scripts/setup-local-gguf.sh
```

## API

| Route | Description |
| --- | --- |
| `GET /` | Dashboard |
| `GET /api/status` | Check all endpoints now; returns the batch (and persists it) |
| `GET /api/history?endpoint=<url>&limit=N` | Most recent stored results for one endpoint, newest first (default 50, max 1000) |
| `GET /api/insights` | Insights for the current batch (AI when configured, rule-based otherwise) |
| `GET` / `POST` / `DELETE /api/endpoints` | List, add (`{"url": ...}`), or remove a monitored URL |

`responseTime` values are Go durations in nanoseconds. Added URLs must be
`http(s)` and at most 2000 characters. The monitored URL list itself lives in
memory: it resets to the defaults when the process restarts, while the stored
history does not.

## Configuration

```bash
# Persistence (unset = in-memory only)
DATABASE_URL="host=localhost port=5432 user=monitor password=password dbname=api_monitor sslmode=disable"

# Monitoring
CHECK_INTERVAL="15s"      # cmd/monitor polling interval
REQUEST_TIMEOUT="5s"
WEB_PORT=8080

# AI (any OpenAI-compatible chat/completions server)
AI_ENABLED=true
AI_BASE_URL="http://localhost:8000"
AI_API_KEY="your-api-key"
AI_MODEL="gpt-oss-20b"
```

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

Tests cover the checker (httptest servers for 200 / 500 / timeout and
concurrency), the AI client (fake OpenAI server, error fallback), the in-memory
store, and the `/api/status`, `/api/history`, `/api/insights` and
`/api/endpoints` handlers.

`PostgresStore` needs a live database, so its tests are skipped unless
`TEST_DATABASE_URL` is set. They run in a throwaway schema and cover the
timestamp round-trip, batch writes containing a long URL, ordering under tied
timestamps, and the in-place migration of a table created by the older schema:

```bash
docker run -d --name pg -e POSTGRES_PASSWORD=pw -p 5432:5432 postgres:15
TEST_DATABASE_URL="host=localhost port=5432 user=postgres password=pw dbname=postgres sslmode=disable" \
  go test ./internal/storage -run Postgres -v
```

### Schema notes

`check_results.checked_at` is `TIMESTAMPTZ` and `url` is `TEXT`. A naive
`TIMESTAMP` column drops the writer's UTC offset and hands the value back
labelled UTC, shifting every timestamp by the local offset; a bounded
`VARCHAR(n)` lets one over-length URL abort the batch transaction and discard
every other endpoint's result. A table created by an earlier version is altered
to these types on startup.
