# 3. The proto package stays `hipstershop`

Status: accepted (owner decision 2026-11-14, application standards section 14).

## Context

The proto package of checkout-service, product-catalog-service and shipping-service is the legacy `hipstershop`; the standard for every other service is `hipstershop.<name>`. The package name is part of the gRPC service name on the wire (`/hipstershop.ProductCatalogService/...`), so renaming it is a breaking change for every client (`frontend`, `checkout-service`, `recommendation-service`).

## Decision

Keep the package `hipstershop` for `productcatalog.proto`. Changes stay additive (new fields or RPCs, no renumbering, no removals), server first; all changes since the Online Boutique version were additive.

## Consequences

- The package name does not follow the pattern of the newer services. This is a recorded exception, not an oversight.
- A rename would need a new package version and a deprecation window.
