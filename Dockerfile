# --- Stage 1: Build ---
# BuildKit sets TARGETOS/TARGETARCH from --platform, so the same Dockerfile builds amd64 and arm64.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS TARGETARCH

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w" \
    -o /product-catalog-service \
    ./cmd/server

# --- Stage 2: Run ---
FROM alpine:3.24
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

# The service has no healthcheck subcommand: TCP connect to the gRPC port with busybox nc (the image has no other client)
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
    CMD nc -z localhost ${PORT:-3550} || exit 1

ENTRYPOINT ["/app/product-catalog-service"]
