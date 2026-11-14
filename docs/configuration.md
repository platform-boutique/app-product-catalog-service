# Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `DATABASE_URL` | **Yes** | — | Postgres connection string, e.g. `postgres://catalog:catalog@product-catalog-db:5432/productcatalog` (add `?sslmode=disable` if the server has no TLS and you want to skip negotiation). **Contains the password → treat as a Secret.** If unset the process logs fatal and exits |
| `PORT` | No | `3550` | gRPC listen port |
| `EXTRA_LATENCY` | No | none | Go duration (e.g. `200ms`) added to every RPC of `ProductCatalogService` — for chaos/latency testing. Invalid value → fatal at startup |
| `METRICS_PORT` | No | `9090` | Port of the Prometheus `/metrics` endpoint (scraped by Alloy). A value that is not a port falls back to `9090` with a warning |
| `PPROF_ADDR` | No | unset (no pprof) | `host:port` of a separate pprof listener, e.g. `127.0.0.1:6060`. Off unless set; never expose it. pprof is never served on the metrics port |
| `LOG_LEVEL` | No | `info` | `debug`, `info`, `warn` or `error`; anything else falls back to `info` with a warning |
| `SERVICE_VERSION` | No | build info, else `dev` | `service.version`, the `version` log field and the `build_info` label. Fallbacks in order: ldflags `-X <module>/internal/obs.Version=...` (and `.Commit=`, `.BuildTime=`), the VCS revision Go embeds in the binary (12 characters, `-dirty` if modified), `dev` |
| `DEPLOYMENT_ENVIRONMENT` | No | `local` | `deployment.environment` on every signal and the Pyroscope tag `environment` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | No | unset (telemetry off) | OTLP/**gRPC** endpoint for **traces, metrics and logs**, e.g. `http://alloy:4317`. Telemetry is on only if this is a valid absolute http(s) URL; empty or malformed means off (one warning, never a crash). The legacy `COLLECTOR_SERVICE_ADDR` and `ENABLE_TRACING` are no longer read |
| `OTEL_SDK_DISABLED` | No | unset | `true` (trimmed, case-insensitive) forces everything off: OTLP export and profiling, even if an endpoint or `PYROSCOPE_ADDR` is set |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | No | `grpc` | Documented knob; only gRPC is implemented, the value is not read |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | No | SDK default (`parentbased_always_on`) | Standard OpenTelemetry sampler variables, read by the SDK (tested for `always_on` and `always_off`) |
| `OTEL_RESOURCE_ATTRIBUTES` | No | unset | Extra resource attributes (`k=v,...`); applied after the defaults, so they can override them |
| `OTEL_SERVICE_NAME` | No | `product-catalog-service` | `service.name`; keep the default, dashboards depend on it |
| `OTEL_METRIC_EXPORT_INTERVAL` | No | `15000` | Milliseconds between metric pushes (also accepts durations such as `15s`) |
| `PYROSCOPE_ADDR` | No | unset (profiling off) | Pyroscope URL (SDK push protocol, not OTLP). The only switch: no default address, `ENABLE_PROFILING=1` alone starts nothing |
| `ENABLE_PROFILING` | No | unset | `0` forces profiling off even with `PYROSCOPE_ADDR`; any other value changes nothing |

Notes:

- **The database user needs DDL rights** on its database (CREATE TABLE / ALTER TABLE), because the service applies its own migrations at start-up. There is no switch to turn migrations off.
- **Startup requires a reachable database.** `InitDB` opens a pgx pool and pings it; on failure the process exits non-zero (no retry). In Kubernetes this shows up as `CrashLoopBackOff` until Postgres is ready (with exponential back-off up to 5 min). Use an init container that waits for Postgres, or accept the restarts.
- The Pyroscope application name is hard-coded to `product-catalog-service`.
- pgx pool uses library defaults (no tuning knobs exposed).
- Traces, metrics and logs share one OTLP/gRPC endpoint (Alloy); profiles go to Pyroscope (via Alloy). `service.instance.id` is the pod hostname, so replicas stay distinct in Mimir.
- `/metrics` is served on `METRICS_PORT` (default `9090`); the same registry is bridged into OTLP, so a scrape and a push carry the same names. pprof only on `PPROF_ADDR`, never on the metrics port (before, `ENABLE_PROFILING`/`PYROSCOPE_ADDR` exposed `/debug/pprof/*` there).
- Logs: the `logrus` loggers write the shared JSON shape to stdout (level from `LOG_LEVEL`) and mirror every record over OTLP; see Observability for the fields.
