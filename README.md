# product-catalog-service

A gRPC service that exposes the product catalog for the platform-demo e-commerce platform. It loads all products from PostgreSQL (`product-catalog-db`) into memory at startup and serves reads from that in-memory copy. It is the most widely used read service in the system — `frontend`, `checkout-service` and `recommendation-service` all call it.

Part of the [platform-demo](https://github.com/platform-boutique) platform — microservices, observability (LGTM stack), GitOps (Argo CD), policy enforcement (Kyverno), infrastructure provisioning (Terraform) and an internal developer portal (Backstage). See the master `README.md` in the platform-demo root for the whole-system view.

## At a glance

| | |
|---|---|
| **Language / runtime** | Go 1.25 (static binary, `CGO_ENABLED=0`), `pgx/v5` |
| **Image** | `mladenovskistefan/product-catalog-service` |
| **Protocol / port** | gRPC (HTTP/2, plaintext) on `3550` |
| **Metrics** | Pushed over OTLP/gRPC (Prometheus registry bridged into OpenTelemetry); `/metrics` on `9090` is still served for debugging |
| **Health** | Standard `grpc.health.v1.Health/Check` on `3550` — static `SERVING`, does **not** reflect DB health |
| **State** | In-memory copy of the `products` table; source of truth is Postgres |
| **Depends on** | `product-catalog-db` (PostgreSQL) — **hard dependency at startup** |
| **Called by** | `frontend`, `checkout-service`, `recommendation-service` |
| **gRPC reflection** | Enabled — `grpcurl` works without `-proto` |

## API

Package `hipstershop`, service `ProductCatalogService` ([`proto/productcatalog.proto`](proto/productcatalog.proto)).

| Method | Request | Response | Behaviour |
|---|---|---|---|
| `ListProducts` | `Empty` | `{ repeated Product products }` | Returns all products |
| `GetProduct` | `{ id }` | `Product` | Exact ID match; gRPC `NOT_FOUND` ("no product with ID …") otherwise |
| `SearchProducts` | `{ query }` | `{ repeated Product results }` | Case-insensitive substring match on **name or description** |

`Product` = `{ id, name, description, picture, Money price_usd, repeated string categories }`. All prices are USD (`units` + `nanos`). `picture` is a relative path such as `/static/img/products/sunglasses.jpg` that is **served by `frontend`**, not by this service.

### Data source

Query run against Postgres on load:

```sql
SELECT id, name, description, picture,
       price_usd_currency_code, price_usd_units, price_usd_nanos, categories
FROM products ORDER BY id
```

`categories` is a comma-separated text column; the service lowercases and splits it. The schema and 9 seed products come from the [`product-catalog-db`](../product-catalog-db) image.

### Caching & reload

- The catalog is loaded once at startup and cached in memory (per pod).
- **SIGUSR1** arms a one-shot reload: the *next* request re-reads Postgres, then the flag clears. **SIGUSR2** disarms it. In Kubernetes: `kubectl exec deploy/product-catalog-service -- kill -USR1 1`.
- If the cache is ever empty, every request re-queries Postgres.
- Database changes are therefore **not** visible until a reload or a pod restart.

## Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `DATABASE_URL` | **Yes** | — | Postgres connection string, e.g. `postgres://catalog:catalog@product-catalog-db:5432/productcatalog` (add `?sslmode=disable` if the server has no TLS and you want to skip negotiation). **Contains the password → treat as a Secret.** If unset the process logs fatal and exits |
| `PORT` | No | `3550` | gRPC listen port |
| `METRICS_PORT` | No | `9090` | Prometheus (+pprof) HTTP port |
| `EXTRA_LATENCY` | No | none | Go duration (e.g. `200ms`) added to every `ListProducts`/`GetProduct`/`SearchProducts` call — for chaos/latency testing. Invalid value → fatal at startup |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | No | unset (telemetry off) | OTLP/**gRPC** endpoint for **traces, metrics and logs** (scheme required), e.g. `http://alloy.monitoring.svc.cluster.local:4317`. Legacy fallback: `COLLECTOR_SERVICE_ADDR` (`host:port`). Telemetry is on iff an endpoint is set |
| `ENABLE_TRACING` | No | — | Legacy switch: `0` forces all telemetry off even if an endpoint is set (it is no longer needed to turn telemetry on) |
| `OTEL_METRIC_EXPORT_INTERVAL` | No | `15000` | Milliseconds between metric pushes (Go also accepts durations such as `15s`) |
| `OTEL_SERVICE_NAME` | No | `product-catalog-service` | `service.name` — becomes the Prometheus `job` and the Loki `service_name`; keep the default, dashboards depend on it |
| `PYROSCOPE_ADDR` | No | `http://pyroscope:4040` | Profiles use the Pyroscope SDK push protocol (**not** OTLP). Point it at Alloy (`http://alloy.monitoring.svc.cluster.local:4040`), which forwards to Pyroscope, or directly at Pyroscope |
| `ENABLE_PROFILING` | No | off | `1` starts the Pyroscope SDK **and** exposes `/debug/pprof/*` on the metrics port |

Notes:

- **Startup requires a reachable database.** `InitDB` opens a pgx pool and pings it; on failure the process exits non-zero (no retry). In Kubernetes this shows up as `CrashLoopBackOff` until Postgres is ready (with exponential back-off up to 5 min). Use an init container that waits for Postgres, or accept the restarts.
- The Pyroscope application name is hard-coded to `product-catalog-service`.
- pgx pool uses library defaults (no tuning knobs exposed).
- Telemetry is **pushed**, not scraped: traces, metrics and logs share one OTLP/gRPC endpoint (Alloy); profiles go to Pyroscope (via Alloy). `service.instance.id` is the pod hostname, so replicas stay distinct in Mimir.
- A Prometheus `/metrics` endpoint is still served on `METRICS_PORT` for debugging, but nothing has to scrape it — the same registry is bridged into OTLP, so metric names are unchanged.
- Logs: the `logrus` logger keeps writing JSON to stdout and additionally mirrors every record over OTLP (with trace ids when a context is attached).

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
| `metrics` | `9090` | TCP (HTTP/1.1) | Optional (debugging/pprof). Not scraped by default — metrics are pushed over OTLP |

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
| **Traces** | OpenTelemetry (`otelgrpc`/`otelhttp` handlers), OTLP/gRPC exporter, `AlwaysSample`, W3C TraceContext + Baggage; the trace context is propagated to every downstream call | Alloy `:4317` → Tempo |
| **Metrics** | The Prometheus default registry (`grpc_server_handled_total`, `grpc_server_handling_seconds_*`, `grpc_server_started_total`, `grpc_server_msg_*`, Go runtime/process) is bridged into OpenTelemetry and pushed over OTLP/gRPC every 15 s — metric names are unchanged. `/metrics` is still served for debugging | Alloy → Mimir |
| **Logs** | `logrus` JSON to stdout **and** an OTLP/gRPC hook (`otellogrus`) (the `internal/catalog` logger is hooked too) | Alloy → Loki |
| **Profiles** | Pyroscope Go SDK (CPU, alloc/inuse objects & space) plus pprof HTTP when profiling is on | Alloy `:4040` → Pyroscope |

Every series carries `job="product-catalog-service"` (from `service.name`) and `instance=<pod name>` (`service.instance.id`), which is what the Grafana dashboards select on. Logs land in Loki with `service_name="product-catalog-service"` and trace/span ids, so a log line links to its trace.

## Running locally

Requirements: Go 1.25+, Docker, a Postgres seeded from [`product-catalog-db`](../product-catalog-db), [`grpcurl`](https://github.com/fullstorydev/grpcurl).

```bash
# 1. database (see ../product-catalog-db)
docker run -d --name catalog-postgres -p 5432:5432 \
  -e POSTGRES_DB=productcatalog -e POSTGRES_USER=catalog -e POSTGRES_PASSWORD=catalog \
  mladenovskistefan/product-catalog-db:latest

# 2. service
export DATABASE_URL="postgres://catalog:catalog@localhost:5432/productcatalog?sslmode=disable"
go run ./cmd/server

# or via Docker
docker build -t product-catalog-service .
docker run --rm --network host -e DATABASE_URL="$DATABASE_URL" product-catalog-service

go test ./...
```

### Manual gRPC testing (reflection is on)

```bash
grpcurl -plaintext localhost:3550 hipstershop.ProductCatalogService/ListProducts
grpcurl -plaintext -d '{"id": "OLJCESPC7Z"}' localhost:3550 hipstershop.ProductCatalogService/GetProduct
grpcurl -plaintext -d '{"query": "kitchen"}'  localhost:3550 hipstershop.ProductCatalogService/SearchProducts
grpcurl -plaintext localhost:3550 grpc.health.v1.Health/Check
curl -s localhost:9090/metrics | grep grpc_server
```

Seed product IDs: `OLJCESPC7Z` (Sunglasses), `66VCHSJNUP` (Tank Top), `1YMWWN1N4O` (Watch), `L9ECAV7KIM` (Loafers), `2ZYFJ3GM2N` (Hairdryer), `0PUK6V6EV0` (Candle Holder), `LS4PSXUNUM` (Salt & Pepper Shakers), `9SIQT8TOJO` (Bamboo Glass Jar), `6E92ZMYYFZ` (Mug).

## Dataset

The new store catalog (117 products in 7 departments) is committed as data, ready for the planned schema/seed migrations. **The service does not read it yet**: it still loads the 9 products from `product-catalog-db`.

| File | What it is |
|---|---|
| `data/seed/products.json` | Products: `id`, `name`, `description`, `brand`, `category` (department id), `source_category`, `categories` (lower-case values for the comma-separated column), `tags`, `price_usd` (`units`/`nanos`), `rating`, `stock`, `weight_grams`, `images`, `thumbnail`, `reviews`, `source_id` |
| `data/seed/categories.json` | Departments (`id`, `name`, `description`, `source_categories`) |
| `data/seed/SOURCES.md` | Source, retrieval date, licence as found, what was changed, assumptions |
| `tools/fetch_dataset.py` | One-off builder: downloads DummyJSON + images, converts, writes the files above (Python 3 + Pillow) |
| `tools/validate_dataset.py` | Checks the files and the referenced images (Python 3 standard library only); exit code 1 on any error |

The images live in the **frontend** repo under `static/img/products/<id>-1.webp`, `<id>-2.webp` and `<id>-thumb.webp`; the paths in the JSON (`/static/img/products/...`) are served by `frontend`. Nothing calls an external API at run time.

Regenerate (only when the dataset should change; it downloads from dummyjson.com):

```bash
python3 -m venv /tmp/dataset-venv && /tmp/dataset-venv/bin/pip install pillow==13.0.0
/tmp/dataset-venv/bin/python tools/fetch_dataset.py \
  --images-dir ../frontend/static/img/products --cache-dir /tmp/dataset-cache
```

Validate (paths assume the frontend repo is checked out next to this one):

```bash
python3 tools/validate_dataset.py --images-dir ../frontend/static/img/products
```

## Project structure

```
├── cmd/server/main.go            # entrypoint: tracing, profiling, signals (SIGUSR1/2), gRPC server, metrics server
├── internal/catalog/
│   ├── loader.go                 # pgx pool (InitDB) + LoadCatalog (SELECT … FROM products)
│   └── service.go                # gRPC handlers, in-memory cache, reload logic
├── proto/                        # productcatalog.proto + generated Go
├── Dockerfile
├── go.mod / go.sum
└── docs/                         # empty placeholder
```

## Known limitations

- Exits if Postgres is unreachable at startup (no retry/back-off in-process).
- Health is static; it doesn't reflect DB availability.
- Cache is per-pod and only refreshed by `SIGUSR1`/restart.
- The reload flag is shared between goroutines without synchronisation (technically a data race; benign in practice).
- `amd64`-only image that runs as root unless the pod overrides it.
