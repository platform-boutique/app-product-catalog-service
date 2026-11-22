# Known limitations

- Exits if Postgres is unreachable at startup (no retry/back-off in-process).
- Health is static; it doesn't reflect DB availability.
- Cache is per-pod and only refreshed by `SIGUSR1`/restart; the seed and migrations run only at start-up.
- The reload flag is an `atomic.Bool` and the cached slices are swapped under a lock; a reload that fails or times out keeps the old cache.
- Products removed from `products.json` stay in the table (only the 9 legacy ids are deleted); delete them by hand or with a new migration.
- A changed seed overwrites every seeded column, including `stock` (fine while no other component writes stock).
- `SearchProducts` still matches only name and description; brand/tag search is in `ListProductsFiltered`.
- Migrations need DDL rights for the service's database user; there is no "migrations off" switch.
- No down-migration is run by the service (the `-- +goose Down` sections exist but were not tested).
- The image is `linux/amd64` only (owner decision 2026-11-14) and runs as the numeric user `10001:10001`.
- The Postgres integration tests (`tests/integration`) are not run in the PR gate (the optional integration job of the shared workflow is off, they need a Postgres): they run locally per `docs/development.md`.
- No business counter: OBS-20 names orders, reservations and payments, none of which this service owns; RED, `build_info`, runtime and `db_pool_*` metrics exist.
