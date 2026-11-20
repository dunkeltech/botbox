# syntax=docker/dockerfile:1

# SPDX-License-Identifier: AGPL-3.0-only

FROM node:24-bookworm-slim AS builder
WORKDIR /app
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
COPY design/tokens.json design/avatar_fixtures.json /design/
ENV BOTBOX_STANDALONE=1 NEXT_TELEMETRY_DISABLED=1 API_BACKEND_URL=http://api:8080
RUN mkdir -p public && npm run build

FROM node:24-bookworm-slim
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT=3000
COPY --from=builder --chown=node:node /app/.next/standalone/ ./
COPY --from=builder --chown=node:node /app/.next/static/ ./.next/static/
COPY --from=builder --chown=node:node /app/public/ ./public/
USER node
CMD ["node", "server.js"]
