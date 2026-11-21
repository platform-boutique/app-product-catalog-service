# 1. The service owns its schema and its seed

Status: accepted (reconstructed from the code and the earlier README).

## Context

The catalog used to depend on the `product-catalog-db` image, whose init script created a 9-row table. The new store catalog has departments, more product columns and 117 products, and the image must keep working in the old setup.

## Decision

The service applies versioned goose migrations (embedded in the binary) at start-up under a Postgres advisory lock, then upserts a seed (`data/seed/products.json`, `categories.json`, embedded) in one transaction. The seed is skipped when the SHA-256 of both files equals the one stored in `catalog_seed`. `00001_products.sql` is `CREATE TABLE IF NOT EXISTS`, so it is a no-op on a database that the old image initialised.

## Consequences

- The database user needs DDL rights, and there is no "migrations off" switch.
- A changed seed overwrites every seeded column, including `stock`; rows that are not in the seed are kept, except the 9 legacy ids.
- Replicas that start together apply each migration and the seed exactly once.
- The product data comes from DummyJSON; see the `NOTICE` file and `data/seed/ATTRIBUTION.md` (the licence of the images is not verified).
