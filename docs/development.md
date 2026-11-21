# Develop & test

## Checks and commands

Requirements: Go 1.26 (`GOTOOLCHAIN=auto` fetches the `go.mod` version; the host has Go 1.22), Docker for the image, Python 3.12 for the docs check. The commands below are the ones the shared pipeline (`platform-demo-workflows`) runs, so a failure can be reproduced without pushing.

| Step | Command | Result on 2026-11-21 |
|---|---|---|
| Format | `gofmt -l .` | run, no files listed |
| Vet and unit tests | `go vet ./... && go test -race -count=1 ./...` | run, passed (5 packages with tests) |
| Lint | `golangci-lint run ./...` (CI pins golangci-lint v2.14.0) | not run (golangci-lint is not installed on the host) |
| Vulnerabilities and security | `govulncheck ./...`, `gosec -exclude-generated ./...` | not run |
| Integration tests (Postgres) | `PG_ADMIN_URL=postgres://postgres:postgres@127.0.0.1:55872/postgres?sslmode=disable go test -tags integration -race -count=1 ./tests/integration/` against a throwaway `postgres:17-alpine` (`docker run -d --rm --name <unique name> --memory 256m -p 127.0.0.1:55872:5432 -e POSTGRES_PASSWORD=postgres postgres:17-alpine`, remove it afterwards); see Tests | run, passed (a throwaway container, removed afterwards) |
| End-to-end tests | `GRPC_HOST=localhost:3550 go test -tags e2e ./tests/e2e/` against a running service | not run |
| Dataset check | `python3 tools/validate_dataset.py --images-dir ../frontend/static/img/products --reviews ../review-service/data/reviews.json` (needs the frontend and review-service repos next to this one) | not run |
| Proto | the `protoc` command is in `docs/development.md` (Regenerating the Go stubs); protoc 3.21.12, protoc-gen-go 1.36.11, protoc-gen-go-grpc 1.6.1 | not run. Changes are additive only, server first |
| Image | `docker build -t platform-demo/product-catalog-service:local .` | not run today |
| Image scan | `trivy image --severity CRITICAL,HIGH --exit-code 1 platform-demo/product-catalog-service:local` | not run today |
| Secret scan | `pre-commit run --all-files` (`.pre-commit-config.yaml`, gitleaks) or `gitleaks git --redact` | run with the gitleaks binary of the pre-commit cache (`gitleaks git --redact`, full history, 86 commits): no leaks found |
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

This was run with the binary (`go build ./cmd/server`) against `postgres:17` and against the `product-catalog-db` image (`platform-demo/product-catalog-db:local`), twice each, and with three replicas started at the same moment on an empty database (one applied, two found nothing to do). The Docker image of this service was **not** built in this change.

### Tests

```bash
go test ./...          # unit tests: filters, sort, paging, bad arguments, categories, seed parsing; embedded seed validates
go vet ./...

# migrations + seed against a real Postgres (each test creates and drops its own database)
docker run -d --rm --name pcs-test-pg -p 127.0.0.1:55871:5432 -e POSTGRES_PASSWORD=postgres postgres:17
PG_ADMIN_URL='postgres://postgres:postgres@127.0.0.1:55871/postgres?sslmode=disable' \
  go test -tags integration ./tests/integration/
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
