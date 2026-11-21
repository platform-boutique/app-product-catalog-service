# Operations

## Health

- `grpc.health.v1.Health/Check` on the gRPC port (`3550`) is `SERVING` as soon as the server listens, which is after migrations, seed and the first load. It never changes afterwards: it does not reflect the database.
- Container probe: `/app/product-catalog-service healthcheck` (exec form, no shell) calls the health service on `127.0.0.1:$PORT` and exits 0 only on `SERVING`.

```bash
grpcurl -plaintext localhost:3550 grpc.health.v1.Health/Check
```

## Start-up needs the database

An unreachable database, a failed migration or a failed seed make the process exit non-zero (no in-process retry). On Kubernetes that is `CrashLoopBackOff` until Postgres is ready. The database user needs DDL rights; there is no switch to turn migrations off.

## Shutdown

On SIGTERM or SIGINT all health services go `NOT_SERVING`, the server stops accepting RPCs and waits up to 5 s for in-flight ones (`GracefulStop`), then closes what is left; telemetry is flushed (cut off after 4 s) and the process exits 0. A signal during start-up also exits 0. Covered by `TestServer_SIGTERMExitsZero` and `TestServer_BlackholedCollectorDoesNotBlockExit` in `tests/integration/` (they need a Postgres).

## Reload of the catalog

Database changes are not visible until a reload or a restart. `SIGUSR1` arms a one-shot reload (the next request re-reads Postgres), `SIGUSR2` disarms it:

```bash
kubectl exec deploy/product-catalog-service -- kill -USR1 1
```

A reload re-reads products and departments only; it does not run migrations or the seed. A reload that fails or times out keeps the old cache.

## Scaling and replicas

Each replica holds its own copy, so replicas can differ until each has been reloaded. Migrations and the seed are serialised by the advisory lock, and starting three replicas at once on an empty database was run (one applied, two found nothing to do; see the README). Resource figures of the local stack are in [Kubernetes](kubernetes.md).

## Failure behaviour

| Failure | Behaviour |
|---|---|
| Postgres down at start | process exits non-zero |
| Postgres goes away while running | reads are served from the cache; a reload fails and keeps the old cache; every query is bounded at 10 s |
| Seed or migration error | process exits non-zero |
| Unknown product id | `NOT_FOUND` |
| Handler panic | turned into `INTERNAL` by a recovery interceptor |
| Collector unreachable | telemetry export fails in the background; start-up does not wait for it |

## Runbook

| Symptom | Check |
|---|---|
| `CrashLoopBackOff` | the log line before the exit: `DATABASE_URL` unset or wrong, Postgres not ready, missing DDL rights, migration error |
| Product data in the database changed but the shop shows the old data | send `SIGUSR1`, then make one request; or restart the pod |
| Replicas show different data | reload or restart each of them |
| Seed changed but nothing happened | the seed is applied only at start-up and only when the checksum of the two JSON files changed (`catalog_seed` table) |
| Products removed from the seed still exist | the seed never deletes (only the 9 legacy Online Boutique ids); remove them by hand or with a migration |

There is no dashboard or alert rule for this service in the repository yet (the dashboards are an open task).
