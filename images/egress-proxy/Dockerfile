# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS builder
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/egress-proxy ./cmd/egress-proxy
FROM debian:bookworm-slim
LABEL botbox.proxy_ingress="bot-network-v1"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/egress-proxy /usr/local/bin/egress-proxy
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/egress-proxy"]
