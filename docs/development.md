# Develop & test

## Checks and commands

Requirements: Go 1.26 (`GOTOOLCHAIN=auto` fetches the `go.mod` version; the host has Go 1.22), Docker for the image, Python 3.12 for the docs check. The commands below are the ones the shared pipeline (`platform-demo-workflows`) runs, so a failure can be reproduced without pushing.

| Step | Command | Result on 2026-11-22 (secret scan and workflow lint: 2026-11-21) |
|---|---|---|
| Format | `gofmt -l .` | run, no files listed |
| Vet and unit tests | `go vet ./... && go test -race -count=1 -cover ./...` | run, passed; coverage cmd/server 34.6%, data 75.0%, internal/catalog 58.6%, internal/healthcheck 87.5%, internal/obs 89.2%; `go vet -tags integration ./...` also clean; the flaky-test fix was run `-race -count=50`: pass |
| Lint | `golangci-lint run ./...` (CI pins golangci-lint v2.14.0; run as `docker run --rm -v "$PWD":/app -w /app golangci/golangci-lint:v2.14.0 golangci-lint run [--build-tags integration] ./...`) | run with v2.14.0, with and without the integration tag: 0 issues (after two unchecked `Close` errors in `tests/integration` were fixed) |
| Vulnerabilities and security | `govulncheck ./...`, `gosec -exclude-generated ./...` | run: govulncheck `No vulnerabilities found`; gosec 0 issues (20 files, 2192 lines) |
| Integration tests (Postgres) | `PG_ADMIN_URL=postgres://postgres:postgres@127.0.0.1:55872/postgres?sslmode=disable DATABASE_URL=postgres://postgres:postgres@127.0.0.1:55872/postgres?sslmode=disable go test -tags integration -race -count=1 ./tests/integration/` against a throwaway `postgres:17` (`docker run -d --rm --name <unique name> --memory 256m -p 127.0.0.1:55872:5432 -e POSTGRES_PASSWORD=postgres postgres:17`, remove it afterwards); see Tests. Without `DATABASE_URL` two tests skip | run on 2026-11-22 with `-race`: all 12 tests pass (also `-count=3`), nothing skipped; the container was removed afterwards |
| End-to-end tests | `GRPC_HOST=localhost:3550 go test -tags e2e ./tests/e2e/` against a running service | run on 2026-11-22 against the final image (started on a throwaway Postgres): passed |
| Dataset check | `python3 tools/validate_dataset.py --images-dir ../frontend/static/img/products --reviews ../review-service/data/reviews.json` (needs the frontend and review-service repos next to this one) | run on 2026-11-22 (frontend checkout on branch `feat/browse`, review-service on `main`): `OK: dataset is valid` (304 image files) |
| Proto | the `protoc` command is in `docs/development.md` (Regenerating the Go stubs); protoc 3.21.12, protoc-gen-go 1.36.11, protoc-gen-go-grpc 1.6.1 | not run (no proto change; the CI Proto job runs `buf lint` and `buf breaking`). Changes are additive only, server first |
| Image | `docker build --platform linux/amd64 --build-arg SERVICE_VERSION=final-test --build-arg SERVICE_COMMIT=<sha> -t platform-demo/product-catalog-service:local .` | run: 44.1 MB; user 10001:10001; container healthy (exec-form HEALTHCHECK) against Postgres, 117 products loaded, `docker stop` exits 0 in under 1 s; `starting` log has `version`, `commit`, `build_time`, `build_info` has version and commit, `db_pool_*` metrics present |
| Image scan | `trivy image --severity CRITICAL,HIGH --exit-code 1 platform-demo/product-catalog-service:local` | run (aquasec/trivy 0.76.1): exit 0, 0 findings (alpine 3.24.4 and the Go binary) |
| Whole shop | `local/up.sh` then `local/smoke.sh` (compose stack with the final image of this service) | run on 2026-11-22 with the final image: `SMOKE PASSED` three times in a row, 87 PASS each; 0 error-level log lines in this service |
| Secret scan | `pre-commit run --all-files` (`.pre-commit-config.yaml`, gitleaks) or `gitleaks git --redact` | run with the gitleaks binary of the pre-commit cache (`gitleaks git --redact --no-banner`, full history): no leaks found on 2026-11-21 and again on 2026-11-22 (95 commits, before the final docs commit) |
| Workflow lint | `actionlint` | run (actionlint 1.7.12), no findings |
| Docs | `mkdocs build --strict` (mkdocs 1.6.1 + mkdocs-techdocs-core 1.7.1, Python 3.12 venv) and the link check `python3 -I tools/check-md-links.py .` from `platform-demo-workflows` (relative links in `README.md` and `docs/`) | run, both passed |

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

This was run with the binary (`go build ./cmd/server`) against `postgres:17` and against the `product-catalog-db` image (`platform-demo/product-catalog-db:local`), twice each, and with three replicas started at the same moment on an empty database (one applied, two found nothing to do). The Docker image was built and run against a throwaway `postgres:17` on 2026-11-22 (see Develop).

### Tests

```bash
go test ./...          # unit tests: filters, sort, paging, bad arguments, categories, seed parsing; embedded seed validates
go vet ./...

# migrations + seed against a real Postgres (each test creates and drops its own database)
docker run -d --rm --name pcs-test-pg --memory 256m -p 127.0.0.1:55871:5432 -e POSTGRES_PASSWORD=postgres postgres:17
U='postgres://postgres:postgres@127.0.0.1:55871/postgres?sslmode=disable'
# DATABASE_URL also enables the two tests that use the service's own pool (without it they skip)
PG_ADMIN_URL=$U DATABASE_URL=$U go test -tags integration ./tests/integration/
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

## Project structure

```
├── cmd/server/main.go            # entrypoint: tracing, profiling, signals (SIGUSR1/2), gRPC server, metrics server
├── internal/healthcheck/healthcheck.go     # `healthcheck` subcommand (container HEALTHCHECK): grpc.health.v1 Check on the own port
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
