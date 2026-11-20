# syntax=docker/dockerfile:1

# SPDX-License-Identifier: AGPL-3.0-only

FROM golang:1.26-bookworm AS builder
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/botbox-api ./cmd/api

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/botbox-api /usr/local/bin/botbox-api
ENTRYPOINT ["/usr/local/bin/botbox-api"]
