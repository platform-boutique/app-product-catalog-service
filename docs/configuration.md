# Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `DATABASE_URL` | **Yes** | — | Postgres connection string, e.g. `postgres://catalog:catalog@product-catalog-db:5432/productcatalog` (add `?sslmode=disable` if the server has no TLS and you want to skip negotiation). **Contains the password → treat as a Secret.** If unset the process logs fatal and exits |
| `PORT` | No | `3550` | gRPC listen port |
| `METRICS_PORT` | No | `9090` | Prometheus (+pprof) HTTP port |
| `EXTRA_LATENCY` | No | none | Go duration (e.g. `200ms`) added to every RPC of `ProductCatalogService` — for chaos/latency testing. Invalid value → fatal at startup |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | No | unset (telemetry off) | OTLP/**gRPC** endpoint for **traces, metrics and logs** (scheme required), e.g. `http://alloy.monitoring.svc.cluster.local:4317`. Legacy fallback: `COLLECTOR_SERVICE_ADDR` (`host:port`). Telemetry is on iff an endpoint is set |
| `ENABLE_TRACING` | No | — | Legacy switch: `0` forces all telemetry off even if an endpoint is set (it is no longer needed to turn telemetry on) |
| `OTEL_METRIC_EXPORT_INTERVAL` | No | `15000` | Milliseconds between metric pushes (Go also accepts durations such as `15s`) |
| `OTEL_SERVICE_NAME` | No | `product-catalog-service` | `service.name` — becomes the Prometheus `job` and the Loki `service_name`; keep the default, dashboards depend on it |
| `PYROSCOPE_ADDR` | No | `http://pyroscope:4040` | Profiles use the Pyroscope SDK push protocol (**not** OTLP). Point it at Alloy (`http://alloy.monitoring.svc.cluster.local:4040`), which forwards to Pyroscope, or directly at Pyroscope |
| `ENABLE_PROFILING` | No | off | `1` starts the Pyroscope SDK **and** exposes `/debug/pprof/*` on the metrics port |

Notes:

- **The database user needs DDL rights** on its database (CREATE TABLE / ALTER TABLE), because the service applies its own migrations at start-up. There is no switch to turn migrations off.
- **Startup requires a reachable database.** `InitDB` opens a pgx pool and pings it; on failure the process exits non-zero (no retry). In Kubernetes this shows up as `CrashLoopBackOff` until Postgres is ready (with exponential back-off up to 5 min). Use an init container that waits for Postgres, or accept the restarts.
- The Pyroscope application name is hard-coded to `product-catalog-service`.
- pgx pool uses library defaults (no tuning knobs exposed).
- Telemetry is **pushed**, not scraped: traces, metrics and logs share one OTLP/gRPC endpoint (Alloy); profiles go to Pyroscope (via Alloy). `service.instance.id` is the pod hostname, so replicas stay distinct in Mimir.
- A Prometheus `/metrics` endpoint is still served on `METRICS_PORT` for debugging, but nothing has to scrape it — the same registry is bridged into OTLP, so metric names are unchanged.
- Logs: the `logrus` logger keeps writing JSON to stdout and additionally mirrors every record over OTLP (with trace ids when a context is attached).
