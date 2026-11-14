# product-catalog-service

A gRPC service that exposes the product catalog for the platform-demo e-commerce platform. It loads all products from PostgreSQL (`product-catalog-db`) into memory at startup and serves reads from that in-memory copy. It is the most widely used read service in the system — `frontend`, `checkout-service` and `recommendation-service` all call it.

Part of the [platform-demo](https://github.com/platform-boutique) platform — microservices, observability (LGTM stack), GitOps (Argo CD), policy enforcement (Kyverno), infrastructure provisioning (Terraform) and an internal developer portal (Backstage). See the master `README.md` in the platform-demo root for the whole-system view.

## At a glance

| | |
|---|---|
| **Language / runtime** | Go (static binary, `CGO_ENABLED=0`), `pgx/v5`, `pressly/goose/v3` |
| **Image** | `mladenovskistefan/product-catalog-service` |
| **Protocol / port** | gRPC (HTTP/2, plaintext) on `3550` |
| **Metrics** | Prometheus `/metrics` on `9090` (`METRICS_PORT`), scraped by Alloy; the same registry is also pushed over OTLP/gRPC when an endpoint is set |
| **Health** | Standard `grpc.health.v1.Health/Check` on `3550` — static `SERVING`, does **not** reflect DB health |
| **State** | In-memory copy of the `products` and `departments` tables; source of truth is Postgres, schema and seed are owned by this service (migrations at start-up) |
| **Depends on** | `product-catalog-db` (PostgreSQL) — **hard dependency at startup** |
| **Called by** | `frontend`, `checkout-service`, `recommendation-service` |
| **gRPC reflection** | Enabled — `grpcurl` works without `-proto` |

## API

Package `hipstershop`, service `ProductCatalogService` ([`proto/productcatalog.proto`](proto/productcatalog.proto)). All changes since the Online Boutique version are additive (new fields and RPCs; existing field numbers and RPCs unchanged), so callers built from the old proto keep working.

| Method | Request | Response | Behaviour |
|---|---|---|---|
| `ListProducts` | `Empty` | `ListProductsResponse { products }` | All products, ordered by id; `total`/`page`/`page_size` stay 0 |
| `GetProduct` | `{ id }` | `Product` | Exact ID match; gRPC `NOT_FOUND` ("no product with ID …") otherwise |
| `SearchProducts` | `{ query }` | `{ repeated Product results }` | Case-insensitive substring match on **name or description** (unchanged) |
| `ListProductsFiltered` | `ListProductsRequest { department, query, sort, page, page_size }` | `ListProductsResponse { products, total, page, page_size }` | Filter, sort and page (details below) |
| `ListCategories` | `Empty` | `ListCategoriesResponse { repeated Category categories }` | Departments in display order, `Category = { id, name, description, product_count }` |

`Product` = `{ id, name, description, picture, Money price_usd, repeated string categories, brand, double rating, int32 review_count, int32 stock, int32 weight_grams, repeated string tags, repeated string images, thumbnail, department }` (fields 7-15 are new). All prices are USD (`units` + `nanos`). `picture`, `images` and `thumbnail` are relative paths such as `/static/img/products/blue-frock-162-1.webp` that are **served by `frontend`**, not by this service. `picture` is the first of `images` (the thumbnail if there are none). `department` is the `Category.id`. `review_count` is always 0 here (reviews will live in their own service); `weight_grams` is a rough assumption of the dataset (see `data/seed/SOURCES.md`).

`ListProductsFiltered` rules:

- `department`: exact, case-insensitive match on `Product.department`; empty = all.
- `query`: trimmed, case-insensitive substring over name, description, brand and each tag; empty = no filter.
- `sort` (enum `ProductSort`): `PRODUCT_SORT_UNSPECIFIED` (= name), `PRODUCT_SORT_NAME_ASC`, `PRODUCT_SORT_PRICE_ASC`, `PRODUCT_SORT_PRICE_DESC`, `PRODUCT_SORT_RATING_DESC`. Ties are broken by name, then id, so pages are stable. An unknown enum value is `INVALID_ARGUMENT`.
- `page` is 1-based (0 = 1); `page_size` 0 = 24, values above 100 are capped at 100; a negative `page` or `page_size` is `INVALID_ARGUMENT`. A page past the end returns no products but the correct `total`. The response carries the effective `page` and `page_size`.

### Data source, schema and seed

The service owns its schema and data:

1. **Migrations** (`internal/catalog/migrations/*.sql`, embedded in the binary) are applied at start-up with [`pressly/goose`](https://github.com/pressly/goose) v3; applied versions are recorded in `goose_db_version`.
   - `00001_products.sql`: the `products` table exactly as the `product-catalog-db` image creates it (`CREATE TABLE IF NOT EXISTS`, so a no-op on a database initialised by that image).
   - `00002_store_schema.sql`: table `departments (id, name, description, sort_order)`; new `products` columns `brand`, `department` (FK to `departments`, NULL allowed), `source_category`, `tags TEXT[]`, `images TEXT[]`, `thumbnail`, `rating`, `review_count`, `stock`, `weight_grams` (all with defaults and range checks); table `catalog_seed` (checksum of the last applied seed). `tags`/`images` are `TEXT[]` rather than JSONB: they are ordered lists of plain strings and pgx scans them straight into `[]string`.
2. **Seed**: `data/seed/products.json` and `data/seed/categories.json` (embedded via the small `data` package) are validated, then upserted (`INSERT … ON CONFLICT (id) DO UPDATE`) in one transaction. Department order = order in `categories.json`. The 9 old Online Boutique rows (`OLJCESPC7Z`, …) are deleted in the same transaction; other rows that are not in the seed are kept. The SHA-256 of both files is stored in `catalog_seed`; when it is unchanged the seed step writes nothing. `reviews` and `source_id` in the JSON are ignored.
3. **Locking**: migrations run under a Postgres session advisory lock (goose `lock.NewPostgresSessionLocker`, retried every 1 s for up to 5 min) and the seed transaction takes `pg_advisory_xact_lock` on the same key, so replicas that start together apply each migration and the seed exactly once; the others wait and then find nothing to do.

This works on a plain `postgres:17` (empty database) and on the existing `product-catalog-db` image (its init script's 9-row table is evolved in place). Both were run, see "Running locally". A seed or migration error makes start-up fail (non-zero exit), like an unreachable database.

On load the service reads every product (`SELECT … FROM products ORDER BY id`) and the departments. `categories` is still a comma-separated text column (the seed writes the `categories` array joined with `,`); the service lowercases and splits it.

### Caching & reload

- The catalog is loaded once at startup and cached in memory (per pod).
- **SIGUSR1** arms a one-shot reload: the *next* request re-reads Postgres, then the flag clears. **SIGUSR2** disarms it. In Kubernetes: `kubectl exec deploy/product-catalog-service -- kill -USR1 1`.
- If the cache is ever empty, every request re-queries Postgres.
- Database changes are therefore **not** visible until a reload or a pod restart. A reload re-reads products and departments; it does not re-run migrations or the seed (that happens only at start-up).
- `ListProductsFiltered` and `ListCategories` work on the cached copy (no SQL per request); `product_count` is computed from the cached products.

## Configuration

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

## Container image

Multi-stage [`Dockerfile`](Dockerfile):

| Stage | Base | What happens |
|---|---|---|
| builder | `golang:1.25-alpine` | `go mod download`, then `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" ./cmd/server` |
| runtime | `alpine:3.21` | single binary at `/app/product-catalog-service` (Alpine rather than `scratch` so pprof/Pyroscope have a normal userland) |

- **Entrypoint:** `/app/product-catalog-service`. `GOTRACEBACK=single`.
- **User: root** — no `USER` in the Dockerfile; the image runs as UID 0 (verified). The binary needs no privileges, so set a non-root `runAsUser` (e.g. `10001`) in Kubernetes.
- **Architecture: `amd64` only** (`GOARCH=amd64` hard-coded) — fails with `exec format error` on `arm64` nodes.
- **EXPOSE:** `3550`, `9090`. **No `HEALTHCHECK`.**
- **Image size:** ~11 MB. `.dockerignore` excludes `vendor/` only.

## Kubernetes deployment notes

**Workload:** stateless `Deployment` (state is in Postgres). Multiple replicas are safe; each keeps its own cache, so a reload signal must be sent to every pod. Caller connections are long-lived HTTP/2 — see "gRPC load balancing" in the master README.

**Ports to declare**

| Name | Port | Protocol | Expose via Service? |
|---|---|---|---|
| `grpc` | `3550` | TCP (HTTP/2) | Yes — callers dial `product-catalog-service:3550` |
| `metrics` | `9090` | TCP (HTTP/1.1) | Prometheus `/metrics` (scraped by Alloy locally; also pushed over OTLP). pprof is not served here |

**Service DNS name expected by callers:** `product-catalog-service` (`frontend`, `checkout-service` and `recommendation-service` all use `product-catalog-service:3550`).

**Init container (recommended)** — avoids a crash loop while Postgres comes up:

```yaml
initContainers:
  - name: wait-for-db
    image: mladenovskistefan/product-catalog-db:latest   # any image with pg_isready, e.g. postgres:17
    command: ["sh", "-c", "until pg_isready -h product-catalog-db -p 5432 -U catalog -d productcatalog; do echo waiting; sleep 2; done"]
```

**Probes** — the health endpoint is static, so these only prove the process is up (the process only starts serving once the DB load succeeded, so "up" implies "has data"):

```yaml
readinessProbe:
  grpc: { port: 3550 }
  periodSeconds: 5
livenessProbe:
  grpc: { port: 3550 }
  periodSeconds: 10
  failureThreshold: 3
```

**Security context**

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 10001        # image runs as root by default; any non-zero UID works
  runAsGroup: 10001
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  capabilities: { drop: ["ALL"] }
```

**Resources** — requests sized from measured idle usage in the test stack (see the chart's `values.yaml` for limits):

```yaml
resources:
  requests: { cpu: 50m, memory: 48Mi }
  limits:   { cpu: "500m", memory: 128Mi }
```

**Config / secrets**

- Put the **entire `DATABASE_URL`** in a `Secret` and inject with `secretKeyRef` (it embeds the Postgres password). Either template it from the same credentials secret the DB uses (`POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`) or store it directly.
- Everything else is plain env.

**Telemetry:** pushed, not scraped — OTLP/gRPC to Alloy (`global.monitoring.otlpEndpoint`), profiles to Alloy `:4040`. Metrics carry `job="product-catalog-service"` and `instance=<pod>`; no scrape annotations or `ServiceMonitor` are needed.

**Helm chart:** [`platform-demo-helm/product-catalog-service`](https://github.com/platform-boutique/platform-demo-helm/tree/main/product-catalog-service) (built on the shared `platform-common` library chart). It ships with HA defaults — 2 replicas,
HPA, PodDisruptionBudget, topology spread, NetworkPolicy, restricted pod security context, probes and the full env contract (see Configuration) —
and was verified in the earlier local phase on a 3-node kind cluster (since deleted) under the `restricted` Pod Security Standard; it has not been verified on EKS yet.

**Deployment and admission policies:** Argo CD deploys the chart from `platform-demo-helm` (`envs/<env>/values/product-catalog-service.yaml` pins the image digest; promotion is a pull request). In the cluster Kyverno enforces, for this service: pods named `app.kubernetes.io/name: product-catalog-service` that run under the ServiceAccount of the same name (Cilium derives the network identity from that label), an image from this repository pinned by digest and signed by our release pipeline, a read-only root filesystem, resource requests and a memory limit, readiness and liveness probes and no API token. The Helm CI evaluates the rendered chart against these policies before merge; see [`platform-demo-helm/policies`](https://github.com/platform-boutique/platform-demo-helm/tree/main/policies).

## Observability

| Signal | Implementation | Destination |
|---|---|---|
| **Traces** | OpenTelemetry (`otelgrpc` handlers), OTLP/gRPC exporter (batch processor, queue bounded at 2048), W3C TraceContext + Baggage propagated to every downstream call. Sampler from `OTEL_TRACES_SAMPLER` (SDK default `parentbased_always_on`) | Alloy `:4317` -> Tempo |
| **Metrics** | The Prometheus default registry: `grpc_server_handled_total`, `grpc_server_handling_seconds_*`, `grpc_server_started_total`, `grpc_server_msg_*`, Go runtime/process. Served as Prometheus text on `:9090/metrics` (`METRICS_PORT`) and, because the same registry is bridged into OpenTelemetry, also pushed over OTLP every 15 s (metric names identical). Plus `build_info{service,version,commit} 1` | Alloy scrapes `:9090` -> Mimir |
| **Logs** | `logrus`, one JSON object per line on stdout, plus an OTLP/gRPC hook (`otellogrus`). The process logger and the `internal/catalog` logger (migrations, seed, loader) are both configured by `obs.Start`, so start-up lines have the same fields as everyone else's (before, the catalog package wrote logrus' default keys `time`/`level`/`msg`). This service logs nothing per request, so `trace_id`/`span_id` appear only if a future call site logs with `log.WithContext(ctx)`; the `x-request-id` of a caller is put into the context by the gRPC interceptor. Nothing personal is logged (the catalog holds products only; the database URL is never logged) | stdout -> Alloy -> Loki; OTLP -> Alloy -> Loki |
| **Profiles** | Pyroscope Go SDK (CPU, alloc/in-use objects and space, goroutines, mutex, block), only with `PYROSCOPE_ADDR`; tags `service_name`, `version`, `environment` | Pyroscope |

**Telemetry contract** (the code is `internal/obs`, the same package in every Go service): telemetry is on only if `OTEL_EXPORTER_OTLP_ENDPOINT` is a valid absolute http(s) URL; `OTEL_SDK_DISABLED=true` forces everything off (profiling too); profiling only with `PYROSCOPE_ADDR`. Start never waits for the collector (the exporters connect in the background) and on SIGTERM the flush is cut off after 4 s, so a blackholed collector cannot hold the process (unit-tested with a listener that never answers: start and shutdown both return within the bound).

**Resource attributes** on every signal: `service.name` (`OTEL_SERVICE_NAME`, default `product-catalog-service`), `service.version` (`SERVICE_VERSION`, else build info, else `dev`), `service.namespace=everly`, `deployment.environment` (`DEPLOYMENT_ENVIRONMENT`, default `local`), `service.instance.id` (hostname). `OTEL_RESOURCE_ATTRIBUTES` is applied last.

**Log shape** (one shape for all services): `timestamp` (RFC 3339, UTC, milliseconds), `severity` (`debug`, `info`, `warning`, `error`), `message`, `service`, `version`, `trace_id` and `span_id` (hex, only while a valid span is active, never empty or all zeros), `request_id` (when the caller sent one), `error` (string, on errors), plus the call's own fields (for example `order_id`). Level from `LOG_LEVEL`. `trace_id`/`span_id` come from the context of the log call: handlers and clients log with `log.WithContext(ctx)`, the formatter reads the span from it. Calls without a context (startup, background work) have no ids. No card data, tokens or e-mail addresses are logged.

**Startup and version**: the first line is `message: "starting"` with `version`, `commit` and `environment`; `version`/`commit` come from `SERVICE_VERSION`, ldflags `-X <module>/internal/obs.Version=` / `.Commit=`, or the VCS data Go embeds (no Dockerfile change was made: without any of them a container image reports `version: "dev"`, `commit: "unknown"`). Panics are logged as one JSON line (`message: "panic"`, `error`) before the process dies; a startup error is one JSON error line, exit 1.

**Shutdown**: SIGTERM/SIGINT marks every health service `NOT_SERVING`, drains in-flight RPCs for at most 5 s (was 10 s), then flushes telemetry (cut off after 4 s), so the process exits 0 inside 10 s even with an unreachable collector (integration-tested with a listener that never answers: 4.0 s). **pprof** is off. `PPROF_ADDR=127.0.0.1:6060` starts a separate listener; it is never on the metrics port.

Every series carries `job="product-catalog-service"` (from `service.name`) and `instance=<pod name>` (`service.instance.id`), which is what the Grafana dashboards select on. Logs land in Loki with `service_name="product-catalog-service"` and trace/span ids, so a log line links to its trace.

**Verification status** (observability step 3): unit-tested (`go test ./cmd/... ./internal/...`) and, against a throwaway Postgres, `go test -tags integration ./tests/integration/` (goose migrations, seed, SIGTERM exit 0, `OTEL_SDK_DISABLED`, blackholed collector). Not run: the compose stack, a live collector or Pyroscope, an image build, the e2e suite (`tests/e2e`).

## Running locally

Requirements: Go (the toolchain named in `go.mod`; with an older local Go set `GOTOOLCHAIN=auto`), Docker, [`grpcurl`](https://github.com/fullstorydev/grpcurl).

```bash
# 1. database: a plain postgres is enough (the service creates the schema and loads the seed)
docker run -d --name catalog-postgres -p 5432:5432 \
  -e POSTGRES_DB=productcatalog -e POSTGRES_USER=catalog -e POSTGRES_PASSWORD=catalog \
  postgres:17
#    ... or the existing image (its 9 old rows are migrated away on first start):
#    mladenovskistefan/product-catalog-db:latest

# 2. service
export DATABASE_URL="postgres://catalog:catalog@localhost:5432/productcatalog?sslmode=disable"
go run ./cmd/server

# or via Docker
docker build -t product-catalog-service .
docker run --rm --network host -e DATABASE_URL="$DATABASE_URL" product-catalog-service
```

Start-up log on an empty database (first start, then a restart):

```
applied migration 00001_products.sql ...
applied migration 00002_store_schema.sql ...
database schema at version 2 (2 migration(s) applied now)
seed applied: 7 departments, 117 products (sha256 a86ccdc1f9f9)
loaded 117 products from postgres
---
database schema at version 2 (0 migration(s) applied now)
seed unchanged (sha256 a86ccdc1f9f9), nothing written
loaded 117 products from postgres
```

This was run with the binary (`go build ./cmd/server`) against `postgres:17` and against the `product-catalog-db` image (`platform-demo/product-catalog-db:local`), twice each, and with three replicas started at the same moment on an empty database (one applied, two found nothing to do). The Docker image of this service was **not** built in this change.

### Tests

```bash
go test ./...          # unit tests: filters, sort, paging, bad arguments, categories, seed parsing; embedded seed validates
go vet ./...

# migrations + seed against a real Postgres (each test creates and drops its own database)
docker run -d --rm --name pcs-test-pg -p 127.0.0.1:55871:5432 -e POSTGRES_PASSWORD=postgres postgres:17
PG_ADMIN_URL='postgres://postgres:postgres@127.0.0.1:55871/postgres?sslmode=disable' \
  go test -tags integration ./tests/integration/
docker rm -f pcs-test-pg

# against a running service (default localhost:3550)
GRPC_HOST=localhost:3550 go test -tags e2e ./tests/e2e/
```

The integration tests cover: a fresh database, a database initialised by the `product-catalog-db` dump (`tests/integration/testdata/legacy_dump.sql`, a copy), a second run that changes nothing (row versions compared), a changed seed that is re-applied, and four concurrent starts. Unit tests before the real dataset use the 3-product fixture in `internal/catalog/testdata/seed`.

### Manual gRPC testing (reflection is on)

```bash
grpcurl -plaintext localhost:3550 hipstershop.ProductCatalogService/ListProducts
grpcurl -plaintext -d '{"id": "blue-frock-162"}' localhost:3550 hipstershop.ProductCatalogService/GetProduct
grpcurl -plaintext -d '{"query": "kitchen"}'  localhost:3550 hipstershop.ProductCatalogService/SearchProducts
grpcurl -plaintext localhost:3550 hipstershop.ProductCatalogService/ListCategories
grpcurl -plaintext -d '{"department": "kitchen", "sort": "PRODUCT_SORT_PRICE_DESC", "page": 2, "page_size": 5}' \
  localhost:3550 hipstershop.ProductCatalogService/ListProductsFiltered
grpcurl -plaintext -d '{"query": "lipstick", "sort": "PRODUCT_SORT_RATING_DESC"}' \
  localhost:3550 hipstershop.ProductCatalogService/ListProductsFiltered
grpcurl -plaintext localhost:3550 grpc.health.v1.Health/Check
curl -s localhost:9090/metrics | grep grpc_server
```

Product ids are slugs from the seed (`<name>-<source id>`, e.g. `blue-frock-162`); department ids: `women`, `men`, `accessories`, `beauty`, `home`, `kitchen`, `sports`. The old ids (`OLJCESPC7Z`, …) no longer exist after the first start.

## Dataset

The new store catalog (117 products in 7 departments) is committed as data, embedded in the binary and loaded into Postgres at start-up (see "Data source, schema and seed").

| File | What it is |
|---|---|
| `data/seed/products.json` | Products: `id`, `name`, `description`, `brand`, `category` (department id), `source_category`, `categories` (lower-case values for the comma-separated column), `tags`, `price_usd` (`units`/`nanos`), `rating` and `review_count` (derived from the review-service seed, see below), `stock`, `weight_grams`, `images`, `thumbnail`, `reviews` (DummyJSON source data, ignored by the service), `source_id` |
| `data/seed/categories.json` | Departments (`id`, `name`, `description`, `source_categories`) |
| `data/seed/SOURCES.md` | Source, retrieval date, licence as found, what was changed, assumptions |
| `tools/fetch_dataset.py` | One-off builder: downloads DummyJSON + images, converts, writes the files above (Python 3 + Pillow) |
| `tools/validate_dataset.py` | Checks the files and the referenced images (Python 3 standard library only); exit code 1 on any error. `--reviews <file>` also checks rating/review_count against the review-service seed |
| `tools/sync_reviews.py` | Writes `rating` (1 decimal, like review-service) and `review_count` into products.json from `apps/review-service/data/reviews.json`; deterministic, idempotent, `--check` for CI |
| `data/seed/ATTRIBUTION.md` | Short attribution / licence-status note (UNVERIFIED) |

The images live in the **frontend** repo under `static/img/products/<id>-1.webp`, `<id>-2.webp` and `<id>-thumb.webp`; the paths in the JSON (`/static/img/products/...`) are served by `frontend`. Nothing calls an external API at run time.

Regenerate (only when the dataset should change; it downloads from dummyjson.com):

```bash
python3 -m venv /tmp/dataset-venv && /tmp/dataset-venv/bin/pip install pillow==13.0.0
/tmp/dataset-venv/bin/python tools/fetch_dataset.py \
  --images-dir ../frontend/static/img/products --cache-dir /tmp/dataset-cache
```

Validate (paths assume the frontend repo is checked out next to this one):

```bash
python3 tools/validate_dataset.py --images-dir ../frontend/static/img/products \
  --reviews ../review-service/data/reviews.json
```

Ratings: `rating` / `review_count` follow the review-service seed (the shop UI shows its averages). After the review seed changes run `python3 tools/sync_reviews.py --reviews ../review-service/data/reviews.json` and commit products.json. `go test ./data/` fails on drift when the review file is found (`REVIEWS_JSON` or `../../review-service/data/reviews.json`) and skips, loudly, when it is not. A re-seed updates existing rows by id (checksum row changes), including `rating` and `review_count`.

## Project structure

```
├── cmd/server/main.go            # entrypoint: tracing, profiling, signals (SIGUSR1/2), gRPC server, metrics server
├── data/
│   ├── embed.go                  # embeds seed/*.json (go:embed cannot reach outside a package dir)
│   └── seed/                     # products.json, categories.json, SOURCES.md
├── internal/catalog/
│   ├── loader.go                 # pgx pool (InitDB), LoadCatalog, LoadDepartments
│   ├── migrate.go                # goose provider + advisory lock
│   ├── migrations/               # 00001_products.sql, 00002_store_schema.sql (embedded)
│   ├── seed.go                   # read/validate/upsert the seed, delete the legacy rows
│   ├── filter.go                 # ListProductsFiltered: filter, sort, page
│   └── service.go                # gRPC handlers, in-memory cache, reload logic
├── proto/                        # productcatalog.proto + generated Go
├── tests/integration/            # migrations/seed against Postgres (build tag integration)
├── tests/e2e/                    # against a running service (build tag e2e)
├── tools/                        # dataset builder and validator (Python)
├── Dockerfile
├── go.mod / go.sum
└── docs/                         # TechDocs pages
```

Regenerating the Go stubs after a proto change (protoc 3.21.12, protoc-gen-go v1.36.11, protoc-gen-go-grpc 1.6.1, the versions in the generated headers):

```bash
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative proto/productcatalog.proto
```

## Shutdown

`SIGTERM` or `SIGINT`: all health services go `NOT_SERVING`, the gRPC server stops accepting RPCs and waits up to 5 s for in-flight ones (`GracefulStop`), then closes what is left; telemetry is flushed (cut off after 4 s) and the process **exits 0**. A signal during start-up (before the database is ready) also exits 0. Run: `TestServer_SIGTERMExitsZero` and `TestServer_BlackholedCollectorDoesNotBlockExit` (real binary, throwaway Postgres) and the `serve` unit tests.

## Known limitations

- Exits if Postgres is unreachable at startup (no retry/back-off in-process).
- Health is static; it doesn't reflect DB availability.
- Cache is per-pod and only refreshed by `SIGUSR1`/restart; the seed and migrations run only at start-up.
- Products removed from `products.json` stay in the table (only the 9 legacy ids are deleted); delete them by hand or with a new migration.
- A changed seed overwrites every seeded column, including `stock` (fine while no other component writes stock).
- `SearchProducts` still matches only name and description; brand/tag search is in `ListProductsFiltered`.
- Migrations need DDL rights for the service's database user; there is no "migrations off" switch.
- No down-migration is run by the service (the `-- +goose Down` sections exist but were not tested).
- `amd64`-only image that runs as root unless the pod overrides it.
