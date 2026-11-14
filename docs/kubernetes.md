# Kubernetes

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
