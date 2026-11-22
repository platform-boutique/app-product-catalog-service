# --- Stage 1: Build ---
# BuildKit sets TARGETOS/TARGETARCH from --platform, so the same Dockerfile builds amd64 and arm64.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS builder
ARG TARGETOS TARGETARCH
# Build identity, linked into internal/obs (read by the service at start)
ARG SERVICE_VERSION=dev
ARG SERVICE_COMMIT=unknown
ARG BUILD_TIME=

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X product-catalog-service/internal/obs.Version=${SERVICE_VERSION} -X product-catalog-service/internal/obs.Commit=${SERVICE_COMMIT} -X product-catalog-service/internal/obs.BuildTime=${BUILD_TIME}" \
    -o /product-catalog-service \
    ./cmd/server

# --- Stage 2: Run ---
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
ARG SERVICE_VERSION=dev
ARG SERVICE_COMMIT=unknown

LABEL org.opencontainers.image.title="product-catalog-service" \
      org.opencontainers.image.source="https://github.com/platform-boutique/app-product-catalog-service" \
      org.opencontainers.image.revision="${SERVICE_COMMIT}" \
      org.opencontainers.image.version="${SERVICE_VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0"

WORKDIR /app

COPY --from=builder /product-catalog-service .

ENV GOTRACEBACK=single \
    SERVICE_VERSION=${SERVICE_VERSION} \
    SERVICE_COMMIT=${SERVICE_COMMIT}

# numeric non-root user (no entry in /etc/passwd needed); matches the charts' runAsUser. Nothing is written to disk.
USER 10001:10001

EXPOSE 3550 9090

# Exec form, no shell and no nc/wget: the binary calls grpc.health.v1 Check on its own port (PORT, default 3550)
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/app/product-catalog-service", "healthcheck"]

ENTRYPOINT ["/app/product-catalog-service"]
