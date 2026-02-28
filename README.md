# Vigil

AI-powered infrastructure alert triage. Vigil receives alerts from Alertmanager, investigates them using an LLM agent that queries your Prometheus metrics and Loki logs, and posts a root cause analysis to Slack.

## How It Works

```
Alertmanager -- webhook --> Vigil API --> Triage Engine --> Slack
                                                |
                                          Claude (LLM)
                                           |       |
                                 PromQL queries  LogQL queries
                                           │       │
                                      Prometheus   Loki
```

When an alert fires, Vigil:

1. **Ingests** the alert via Alertmanager webhook (`POST /api/v1/alerts`)
2. **Deduplicates** by fingerprint - concurrent triages for the same alert are skipped
3. **Dispatches** an async triage with a linked trace span
4. **Investigates** using an agentic LLM loop - Claude calls tools to query Prometheus metrics and Loki logs, iterating until it has enough context
5. **Enforces budgets** - 15 tool calls max, 200K input / 50K output token limits to prevent runaway costs
6. **Persists** the full conversation (every turn, tool call, and token count) to PostgreSQL
7. **Notifies** via Slack with a formatted root cause analysis

## Observability

Vigil is heavily instrumented:

- **Tracing** - OpenTelemetry with per-LLM-call, per-tool-call, and per-database-call spans, semantic `gen_ai.*` attributes, and span-linked async dispatch. Span events record full raw inputs/outputs from LLM and tool calls.
- **Profiling** - Continuous profiling is enabled via pyroscope. Pyroscope OTEL integration correlates traces to CPU profiles.
- **Metrics** - Prometheus histograms for triage duration, token usage (input/output), tool call counts, and per-query database latency. Build info and profiling status gauges.
- **Logging** - Structured slog with context propagation. Every LLM response, tool execution, and database action logged with duration, token counts, and model info.
- **Ops server** - Separate listener for `/metrics`, `/-/healthy`, `/-/ready`, and pprof. Isolated from api traffic.

## Architecture

```
cmd/server/main.go          Entry point, wiring, HTTP stack, graceful shutdown
internal/
  alertapi/                  HTTP handlers (chi router)
  authmw/                    Bearer token authentication middleware
  cfg/                       Configuration (flags, env vars, validation)
  llm/claude/                Claude API client (Anthropic SDK)
  notify/slack/              Slack webhook notifications
  postgres/                  Connection pool, query tracing
  tools/                     LLM tool registry
    prometheus.go              query_metrics (instant PromQL)
    prometheus_range.go        query_metrics_range (range PromQL)
    loki.go                    query_logs (LogQL)
  triage/
    engine.go                  Agentic LLM loop with tool execution
    service.go                 Deduplication, lifecycle, async dispatch
    store.go                   Storage interface
    memstore/                  In-memory store (development)
    pgstore/                   PostgreSQL store (production)
    triage_metrics.go          Prometheus instrumentation
```

## API

All `/api/v1/*` routes require a bearer token (`Authorization: Bearer <token>`).

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/alerts` | Ingest Alertmanager webhook |
| `GET` | `/api/v1/triage/{id}` | Retrieve triage result |
| `GET` | `/-/healthy` | Liveness probe (always 200 if running) |
| `GET` | `/-/ready` | Readiness probe (fails during shutdown drain) |

## Configuration

All flags can be set via environment variables with a `VIGIL_` prefix (e.g., `VIGIL_CLAUDE_API_KEY`). Env vars do not override explicit CLI flags.

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `-api-token` | `VIGIL_API_TOKEN` | (required) | Bearer token for API authentication |
| `-claude-api-key` | `VIGIL_CLAUDE_API_KEY` | (required) | Anthropic API key |
| `-claude-model` | `VIGIL_CLAUDE_MODEL` | `claude-sonnet-4-20250514` | Claude model |
| `-prometheus-endpoint` | `VIGIL_PROMETHEUS_ENDPOINT` | (required) | Prometheus/Mimir query URL |
| `-prometheus-tenant-id` | `VIGIL_PROMETHEUS_TENANT_ID` | | Tenant ID for multi-tenant Prometheus |
| `-loki-endpoint` | `VIGIL_LOKI_ENDPOINT` | | Loki query URL |
| `-loki-tenant-id` | `VIGIL_LOKI_TENANT_ID` | | Tenant ID for multi-tenant Loki |
| `-database-url` | `VIGIL_DATABASE_URL` | | PostgreSQL URL (empty = in-memory) |
| `-slack-webhook-url` | `VIGIL_SLACK_WEBHOOK_URL` | | Slack incoming webhook |
| `-http-port` | `VIGIL_HTTP_PORT` | `8080` | API listen port |
| `-drain-seconds` | `VIGIL_DRAIN_SECONDS` | `60` | Drain period before shutdown |
| `-shutdown-budget-seconds` | `VIGIL_SHUTDOWN_BUDGET_SECONDS` | `90` | Total shutdown timeout (must > drain) |

## Development

```bash
make build    # compile to ./vigil-server
make test     # go test -race -count=1 ./...
make fuzz     # go test -fuzz=<func> -fuzztime=30s <package>
make lint     # golangci-lint (47 linters)
make cover    # tests with coverage (70% threshold)
make check    # full CI: tidy + vet + lint + cover
```

Run without a database (in-memory store):

```bash
export VIGIL_API_TOKEN="dev-token"
export VIGIL_CLAUDE_API_KEY="sk-ant-..."
export VIGIL_PROMETHEUS_ENDPOINT="http://localhost:9090"
make run
```

Send a test alert:

```bash
curl -X POST http://localhost:8080/api/v1/alerts \
  -H "Authorization: Bearer dev-token" \
  -H "Content-Type: application/json" \
  -d '{
    "alerts": [{
      "status": "firing",
      "fingerprint": "abc123",
      "labels": {"alertname": "HighCPU", "severity": "critical", "instance": "web-1"},
      "annotations": {"summary": "CPU usage above 90% for 5 minutes"}
    }]
  }'
```

## Shutdown

Vigil implements a graceful shutdown sequence:

1. Receive `SIGINT`/`SIGTERM`
2. Close shutdown gate (readiness probe starts failing, load balancer drains)
3. Sleep for drain period (default 60s) - a second signal skips this
4. Shut down components with per-component timeout budget
5. Flush logger, profiler, and OTEL exporter

## Roadmap

**Rate limiting**
- Per-IP, per-API-key, per-LLM-provider, per-tool, and system-wide rate limits

**Two-tier evaluation**
- Haiku-powered pre-triage gate that runs a lightweight eval loop (2-3 tool calls) to classify alerts as TRIAGE, IGNORE, or AUTO_RESOLVE before committing to a full Sonnet/Opus triage
- Same engine loop, smaller tool budget, different system prompt - reuses existing architecture
- Reduces average per-alert cost ~80% by filtering noise before expensive triage runs

**Broader triage sources**
- Accept triage requests beyond Alertmanager - slow database queries, slow HTTP requests, anomaly detectors
- Slack-triggered triage (`@vigil triage`) instead of only webhook-driven

**More investigation tools**
- Tempo traces - query correlated spans for more context into individual traces
- Pyroscope profiles - pull CPU/memory profiles for the affected service and time window; compare across time windows to surface performance regressions
- Runbooks as callable tools - the LLM can follow documented remediation steps
- Safe shell commands - pre-defined, allowlisted commands the LLM can execute securely

**Historical context**
- Feed prior triage history for the same alert fingerprint into the LLM
- Include past resolutions and outcomes to improve future analysis

**Model selection**
- Support multiple LLM providers (not just Claude)
- Route to model based on alert severity or allow caller to specify via API, or two-tier eval can suggest model

**Prompt & tool evaluation**
- Log full conversation histories and replay them against updated prompts to measure improvement
- Iterate on system prompts and tool descriptions with measurable before/after comparison

## Tech Stack
- **[Build-System](https://github.com/keithlinneman/build-system)** - Built and deployed via attested CI/CD pipeline with cryptographic signing, build provenance, and SBOM generation
- **[LinnemanLabs Go-Core](https://github.com/linnemanlabs/go-core)** - Libraries for application boiler-plate code
- **Go** - All application code
- **Claude** - (Anthropic SDK) for LLM reasoning
- **PostgreSQL** - (pgx/v5 with connection pooling) for data persistence
- **OpenTelemetry** - Tracing instrumentation
- **Pyroscope** - Profiling instrumentation
- **Prometheus** - Metrics instrumentation
- **47 golangci-lint rules** - Code review

## Author

Built by [Keith Linneman](https://linnemanlabs.com) at LinnemanLabs.

## License

MIT. Do what you want with it.
