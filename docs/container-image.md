# Container image

Multi-stage [`Dockerfile`](https://github.com/platform-boutique/app-product-catalog-service/tree/main/Dockerfile):

| Stage | Base | What happens |
|---|---|---|
| builder | `golang:1.26-alpine` (pinned by tag and digest) | `go mod download`, then `CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w -X …/internal/obs.Version=… -X …/internal/obs.Commit=… -X …/internal/obs.BuildTime=…" ./cmd/server` |
| runtime | `alpine:3.24` (pinned by tag and digest) | single binary at `/app/product-catalog-service` (Alpine rather than `scratch` so pprof/Pyroscope have a normal userland) |

- **Entrypoint:** `/app/product-catalog-service`. `GOTRACEBACK=single`.
- **User:** numeric `USER 10001:10001`; the process writes nothing to disk, so a read-only root filesystem works.
- **Build identity:** build args `SERVICE_VERSION`, `SERVICE_COMMIT` and `BUILD_TIME` are linked into `internal/obs`; the first two also set the OCI labels `version`/`revision` and the ENV of the same names (the labels `source` and `licenses=Apache-2.0` are fixed). The `starting` log line shows `version`, `commit` and `build_time`; `build_info` carries `service`, `version` and `commit`.
- **Architecture:** `linux/amd64` only (owner decision 2026-11-14); an arm64 build is not run.
- **EXPOSE:** `3550`, `9090`. **`HEALTHCHECK`:** exec form `["/app/product-catalog-service", "healthcheck"]` (gRPC `grpc.health.v1` Check on `127.0.0.1:${PORT:-3550}`, 3 s deadline, no shell tools).
- **Image size:** 44.1 MB (`docker images`, 2026-11-22). `.dockerignore` keeps `.git`, docs, tests, tools and CI files out of the build context.
