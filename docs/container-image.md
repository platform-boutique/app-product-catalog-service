# Container image

Multi-stage [`Dockerfile`](https://github.com/platform-boutique/app-product-catalog-service/tree/main/Dockerfile):

| Stage | Base | What happens |
|---|---|---|
| builder | `golang:1.26-alpine` | `go mod download`, then `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" ./cmd/server` |
| runtime | `alpine:3.21` | single binary at `/app/product-catalog-service` (Alpine rather than `scratch` so pprof/Pyroscope have a normal userland) |

- **Entrypoint:** `/app/product-catalog-service`. `GOTRACEBACK=single`.
- **User: root** — no `USER` in the Dockerfile; the image runs as UID 0 (verified). The binary needs no privileges, so set a non-root `runAsUser` (e.g. `10001`) in Kubernetes.
- **Architecture: `amd64` only** (`GOARCH=amd64` hard-coded) — fails with `exec format error` on `arm64` nodes.
- **EXPOSE:** `3550`, `9090`. **No `HEALTHCHECK`.**
- **Image size:** ~11 MB. `.dockerignore` excludes `vendor/` only.
