# syntax=docker/dockerfile:1

# SPDX-License-Identifier: AGPL-3.0-only

FROM golang:1.26-bookworm AS builder
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/auth-broker ./cmd/auth-broker

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    chromium \
    curl \
    fonts-liberation \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/auth-broker /usr/local/bin/auth-broker
USER 65532:65532
EXPOSE 8085
ENTRYPOINT ["/usr/local/bin/auth-broker"]
