# product-catalog-service

<div class="pd-hero" markdown>
<span class="pd-kicker">service · platform-demo</span>

A gRPC service that exposes the product catalog for the platform-demo e-commerce platform. It loads all products from PostgreSQL (`product-catalog-db`) into memory at startup and serves reads from that in-memory copy. It is the most widely used read service in the system — `frontend`, `checkout-service` and `recommendation-service` all call it.
</div>

<div class="pd-facts" markdown>
<div><span>Runtime</span><strong>Go 1.26</strong><small>Go</small></div>
<div><span>Listens on</span><strong>3550</strong><small>gRPC</small></div>
<div><span>State</span><strong>Stateless</strong></div>
<div><span>Image</span><strong>product-catalog-service</strong><small>mladenovskistefan/</small></div>
</div>

## Where it sits

Called by `frontend`, `checkout-service`, `recommendation-service`. It calls `product-catalog-db`.

```mermaid
flowchart LR
  product_catalog_service["<b>product-catalog-service</b><br/>3550 (gRPC)"]:::me
  frontend["frontend"] --> product_catalog_service
  checkout_service["checkout-service"] --> product_catalog_service
  recommendation_service["recommendation-service"] --> product_catalog_service
  product_catalog_service --> product_catalog_db["product-catalog-db"]
  classDef me stroke-width:3px;
```

## At a glance

| | |
|---|---|
| **Language / runtime** | Go 1.26 (static binary, `CGO_ENABLED=0`), `pgx/v5` |
| **Image** | `mladenovskistefan/product-catalog-service` |
| **Protocol / port** | gRPC (HTTP/2, plaintext) on `3550` |
| **Metrics** | Prometheus `/metrics` on `9090` (`METRICS_PORT`), scraped by Alloy; the same registry is also pushed over OTLP/gRPC when an endpoint is set |
| **Health** | Standard `grpc.health.v1.Health/Check` on `3550` — static `SERVING`, does **not** reflect DB health |
| **State** | In-memory copy of the `products` table; source of truth is Postgres |
| **Depends on** | `product-catalog-db` (PostgreSQL) — **hard dependency at startup** |
| **Called by** | `frontend`, `checkout-service`, `recommendation-service` |
| **gRPC reflection** | Enabled — `grpcurl` works without `-proto` |

## Find it elsewhere

<div class="pd-cards" markdown>
<div><h4><a href="https://argocd.platform-demo.click/applications/argocd/dev-product-catalog-service">Argo CD</a></h4><p>Sync and health of <code>dev-product-catalog-service</code>.</p></div>
<div><h4><a href="https://github.com/platform-boutique/app-product-catalog-service">Repository</a></h4><p>Source, pull requests and releases of <code>app-product-catalog-service</code>.</p></div>
</div>

!!! info "Part of the platform-demo platform"
    Cross-cutting facts (service wiring, observability contract, Kubernetes translation, CI/CD) are in the
    [master reference](https://github.com/platform-boutique/platform-demo-docs). This site only describes product-catalog-service.
