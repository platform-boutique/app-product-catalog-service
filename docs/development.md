# Develop & test

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
