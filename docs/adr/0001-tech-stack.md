<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0001. Core Tech Stack and Development Environment

- **Date:** 2026-11-14
- **Status:** Accepted

## Context

bot.Box needs a high-concurrency control plane for managing sandboxed Linux desktops (Docker first, Kubernetes later), proxying streaming LLM outputs and VNC/PTY WebSockets, enforcing workspace isolation and write-only secret injection in an egress proxy, and providing a rich, accessible, translatable web UI. At the same time, contributors should not need to install conflicting global toolchains or run a parallel `docker-compose` stack for app dependencies.

## Options Considered

### Backend: Go (`chi`, `pgx` + `sqlc`, `goose`, `coder/websocket`, `buf` + gRPC) vs. TypeScript/Node backend

- **Go (`chi` + `sqlc` + `pgx`):** Single static binaries (`cmd/api`, `cmd/agent`, `cmd/egress-proxy`), straightforward concurrency for long-lived VNC/PTY/chat WebSocket relays and HTTP CONNECT/MITM egress proxying, official Docker Engine and Kubernetes `client-go` SDKs, and compile-time type-safe SQL via `sqlc` without ORM reflection magic.
- **Node/TypeScript backend:** Shares types with Next.js directly, but handles raw TCP/TLS proxy splicing and container/gRPC runtime streaming less cleanly, and lacks first-class Kubernetes `client-go` parity.

### Frontend: Next.js App Router + Tailwind + `next-intl` + TanStack Query

- **Next.js App Router (TypeScript `strict`):** Server Components by default, built-in `next/font/google` for Lato, Tailwind mapped directly to `design/tokens.json`, `next-intl` enforcing externalized UI strings from day one, TanStack Query for client API state, and `@novnc/novnc` / `xterm.js` integration.

### Messaging & Dev Environment: NATS JetStream + `devenv` (Nix) + Caddy

- **NATS with JetStream:** Lightweight broker for cross-instance real-time fan-out and durable per-bot work queues without JVM overhead.
- **devenv (Nix) + Caddy:** Pins Go, Node 24, Postgres, NATS, code generators (`sqlc`, `goose`, `buf`, `oapi-codegen`), `golangci-lint`, Playwright browsers, and Kubernetes tools in `devenv.nix`, fronted by Caddy on a single origin so WebSockets and cookies work identically in dev and production.

## Decision

Adopt the following stack for Phase 0.

- **Backend (`backend/`):** Go with `chi` for HTTP routing, `pgx/v5` + `sqlc` for SQL access, `goose` for forward-only migrations, `slog` for structured JSON logging, `coder/websocket` for WebSocket relays, gRPC generated via `buf` (`protoc-gen-go`, `protoc-gen-go-grpc`), and `oapi-codegen` for typed REST handlers.
- **Frontend (`frontend/`):** Next.js (App Router, `strict` TypeScript), Tailwind CSS sourced from `design/tokens.json`, `next-intl` (`messages/en.json`), TanStack Query, and `lucide-react`.
- **State & Events:** PostgreSQL and NATS with JetStream.
- **Tooling & Local Proxy:** Reproducible `devenv.nix` (Nix) providing all toolchains, Postgres, NATS, and `caddy` as a same-origin local proxy (`/api/*` -> Go API, `/` -> Next.js).

## Consequences

- Every contributor gets an identical, reproducible environment via `direnv allow` or `devenv shell` and starts the full stack with `devenv up`.
- API and database contracts rely on explicit code generation (`make gen`) rather than ad-hoc runtime types, enforced in CI alongside Go/TS linters and an ESLint rule forbidding hard-coded JSX UI copy.
- Only a host Docker Engine is required outside Nix for running bot sandboxes and testcontainers.
