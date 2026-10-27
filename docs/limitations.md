# Known limitations

- Exits if Postgres is unreachable at startup (no retry/back-off in-process).
- Health is static; it doesn't reflect DB availability.
- Cache is per-pod and only refreshed by `SIGUSR1`/restart; the seed and migrations run only at start-up.
- The reload flag is shared between goroutines without synchronisation (technically a data race; benign in practice). The cached slices themselves are now swapped under a lock.
- Products removed from `products.json` stay in the table (only the 9 legacy ids are deleted); delete them by hand or with a new migration.
- A changed seed overwrites every seeded column, including `stock` (fine while no other component writes stock).
- `SearchProducts` still matches only name and description; brand/tag search is in `ListProductsFiltered`.
- Migrations need DDL rights for the service's database user; there is no "migrations off" switch.
- No down-migration is run by the service (the `-- +goose Down` sections exist but were not tested).
- `amd64`-only image that runs as root unless the pod overrides it.
