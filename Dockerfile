# Export-friendly image build (same as deployments/containers/Containerfile).
ARG BUILD_VERSION=dev
FROM docker.io/library/golang:1.23-bookworm AS go-builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.buildVersion=${BUILD_VERSION}" -o /rf2vc ./cmd/gateway

FROM docker.io/library/debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/ \
    && useradd -u 65532 -r -s /usr/sbin/nologin appuser \
    && mkdir -p /data/iso-cache && chown -R 65532:65532 /data
USER 65532
COPY --from=go-builder /rf2vc /app/rf2vc
ENV RF2VC_LISTEN=:8080
ENV RF2VC_DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/app/rf2vc"]
CMD ["-config", "/etc/rf2vc/gateway.yaml"]
