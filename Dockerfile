# syntax=docker/dockerfile:1
# document-service: pure-Go, CGO-free static binary (CON-280). Unlike pdf-service
# (native libpdfium), it has no native dependencies, so it ships on
# distroless/static as a non-root scratch-like image. linux/amd64.

# ─── build ───────────────────────────────────────────────────────────────────
FROM golang:1.25-bookworm AS build
WORKDIR /app

# gRPC health probe for the runtime image (no shell / HTTP endpoint there).
ARG GRPC_HEALTH_PROBE_VERSION=v0.4.34
RUN set -eux; \
    wget -qO /usr/local/bin/grpc_health_probe \
      "https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/${GRPC_HEALTH_PROBE_VERSION}/grpc_health_probe-linux-amd64"; \
    chmod +x /usr/local/bin/grpc_health_probe

COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /documents-service ./cmd/documents-service

# ─── runtime ─────────────────────────────────────────────────────────────────
# distroless/static (no libc needed for a CGO-free binary); the :nonroot tag runs
# as an unprivileged user and bundles ca-certificates.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /usr/local/bin/grpc_health_probe /usr/local/bin/grpc_health_probe
COPY --from=build /documents-service /usr/local/bin/documents-service

ENV DOCUMENTS_SERVICE_LISTEN=":50051"
EXPOSE 50051

# Private-network only — orchestrators probe gRPC health via grpc_health_probe.
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 \
  CMD ["/usr/local/bin/grpc_health_probe", "-addr=:50051"]

ENTRYPOINT ["/usr/local/bin/documents-service"]
