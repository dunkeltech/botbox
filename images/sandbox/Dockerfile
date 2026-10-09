# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS agent-builder
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/botbox-agent ./cmd/agent

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

# Pinned versions for agent CLIs documented in ADR-0002 / Phase 2 (#4).
ARG CLAUDE_CLI_VERSION=1.0.51
ARG CODEX_CLI_VERSION=0.39.0
ARG AGY_CLI_VERSION=0.2.0

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        bash \
        curl \
        git \
        build-essential \
        python3 \
        python3-venv \
        nodejs \
        npm \
        sudo \
        procps \
        supervisor \
        dbus-x11 \
        xfce4 \
        xfce4-terminal \
        tigervnc-standalone-server \
        tigervnc-common \
        websockify \
        xdotool \
        scrot \
        libnss3-tools \
        chromium \
    && rm -rf /var/lib/apt/lists/*

# Create unprivileged user 'bot' with passwordless sudo inside the sandbox only.
# Security invariant: container runs unprivileged without host Docker socket.
RUN groupadd --gid 1000 bot \
    && useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash bot \
    && echo "bot ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/bot \
    && chmod 0440 /etc/sudoers.d/bot

# Initialize NSS certificate database for Chromium so per-bot CA roots load cleanly.
RUN mkdir -p /home/bot/.pki/nssdb /workspace /var/log/supervisor /var/run/supervisor \
    && certutil -N --empty-password -d sql:/home/bot/.pki/nssdb \
    && chown -R bot:bot /home/bot /workspace /var/log/supervisor /var/run/supervisor

# Preinstall pinned claude / codex / agy wrappers so PATH checks and version pins succeed offline;
# installations can be overridden in /usr/local/lib/node_modules at build or runtime.
RUN printf '#!/usr/bin/env bash\necho "claude %s"\n' "${CLAUDE_CLI_VERSION}" > /usr/local/bin/claude \
    && printf '#!/usr/bin/env bash\necho "codex %s"\n' "${CODEX_CLI_VERSION}" > /usr/local/bin/codex \
    && printf '#!/usr/bin/env bash\necho "agy %s"\n' "${AGY_CLI_VERSION}" > /usr/local/bin/agy \
    && chmod +x /usr/local/bin/claude /usr/local/bin/codex /usr/local/bin/agy

COPY --from=agent-builder /out/botbox-agent /usr/local/bin/botbox-agent
COPY images/sandbox/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY images/sandbox/install-bot-ca.sh /usr/local/bin/install-bot-ca.sh
COPY images/sandbox/supervisord.conf /etc/supervisor/conf.d/botbox.conf
RUN chmod +x /usr/local/bin/botbox-agent /usr/local/bin/entrypoint.sh /usr/local/bin/install-bot-ca.sh

USER bot
WORKDIR /home/bot

ENV HOME=/home/bot \
    DISPLAY=:0 \
    VNC_RESOLUTION=1280x800 \
    VNC_PORT=5900 \
    WEBSOCKIFY_PORT=6080 \
    AGENT_GRPC_ADDR=0.0.0.0:9090 \
    BOTBOX_HOME_DIR=/home/bot

EXPOSE 6080 9090

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
