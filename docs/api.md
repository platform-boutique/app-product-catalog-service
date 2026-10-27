# API

Package `hipstershop`, service `ProductCatalogService` ([`proto/productcatalog.proto`](https://github.com/platform-boutique/app-product-catalog-service/blob/main/proto/productcatalog.proto)). All changes since the Online Boutique version are additive (new fields and RPCs; existing field numbers and RPCs unchanged), so callers built from the old proto keep working.

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

## Data source, schema and seed

The service owns its schema and data:

1. **Migrations** (`internal/catalog/migrations/*.sql`, embedded in the binary) are applied at start-up with [`pressly/goose`](https://github.com/pressly/goose) v3; applied versions are recorded in `goose_db_version`.
   - `00001_products.sql`: the `products` table exactly as the `product-catalog-db` image creates it (`CREATE TABLE IF NOT EXISTS`, so a no-op on a database initialised by that image).
   - `00002_store_schema.sql`: table `departments (id, name, description, sort_order)`; new `products` columns `brand`, `department` (FK to `departments`, NULL allowed), `source_category`, `tags TEXT[]`, `images TEXT[]`, `thumbnail`, `rating`, `review_count`, `stock`, `weight_grams` (all with defaults and range checks); table `catalog_seed` (checksum of the last applied seed). `tags`/`images` are `TEXT[]` rather than JSONB: they are ordered lists of plain strings and pgx scans them straight into `[]string`.
2. **Seed**: `data/seed/products.json` and `data/seed/categories.json` (embedded via the small `data` package) are validated, then upserted (`INSERT … ON CONFLICT (id) DO UPDATE`) in one transaction. Department order = order in `categories.json`. The 9 old Online Boutique rows (`OLJCESPC7Z`, …) are deleted in the same transaction; other rows that are not in the seed are kept. The SHA-256 of both files is stored in `catalog_seed`; when it is unchanged the seed step writes nothing. `reviews` and `source_id` in the JSON are ignored.
3. **Locking**: migrations run under a Postgres session advisory lock (goose `lock.NewPostgresSessionLocker`, retried every 1 s for up to 5 min) and the seed transaction takes `pg_advisory_xact_lock` on the same key, so replicas that start together apply each migration and the seed exactly once; the others wait and then find nothing to do.

This works on a plain `postgres:17` (empty database) and on the existing `product-catalog-db` image (its init script's 9-row table is evolved in place). Both were run, see "Running locally". A seed or migration error makes start-up fail (non-zero exit), like an unreachable database.

On load the service reads every product (`SELECT … FROM products ORDER BY id`) and the departments. `categories` is still a comma-separated text column (the seed writes the `categories` array joined with `,`); the service lowercases and splits it.

## Caching & reload

- The catalog is loaded once at startup and cached in memory (per pod).
- **SIGUSR1** arms a one-shot reload: the *next* request re-reads Postgres, then the flag clears. **SIGUSR2** disarms it. In Kubernetes: `kubectl exec deploy/product-catalog-service -- kill -USR1 1`.
- If the cache is ever empty, every request re-queries Postgres.
- Database changes are therefore **not** visible until a reload or a pod restart. A reload re-reads products and departments; it does not re-run migrations or the seed (that happens only at start-up).
- `ListProductsFiltered` and `ListCategories` work on the cached copy (no SQL per request); `product_count` is computed from the cached products.
