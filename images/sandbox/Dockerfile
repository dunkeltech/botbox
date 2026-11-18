# syntax=docker/dockerfile:1

# SPDX-License-Identifier: AGPL-3.0-only

FROM node:24-bookworm-slim AS node-runtime

FROM golang:1.26-bookworm AS agent-builder
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/botbox-agent ./cmd/agent
COPY --from=node-runtime /usr/local/bin/node /out/node
RUN strip --strip-unneeded /out/node

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

# Pinned agent CLIs. Claude Code 2.1.323 is the native package; its
# postinstall must run or bin/claude.exe stays a text stub (ADR-0012).
ARG CLAUDE_CLI_VERSION=2.1.323
ARG CODEX_CLI_VERSION=0.155.0
ARG PI_CLI_VERSION=1.5.2
# agy release URLs and SHA-512 digests are pinned in releases/.

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        bash \
        curl \
        git \
        build-essential \
        python3 \
        python3-venv \
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

# Use the Node 24 toolchain for the CLIs and interactive tools. Keeping the
# Debian Node and npm packages as well would exceed the sandbox image budget.
COPY --from=agent-builder /out/node /usr/local/bin/node
COPY --from=node-runtime /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && ln -s /usr/local/bin/node /usr/bin/nodejs \
    && ln -s /usr/local/bin/node /usr/bin/node \
    && ln -s /usr/local/bin/npm /usr/bin/npm \
    && ln -s /usr/local/bin/npx /usr/bin/npx

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

# Install real pinned executables; version-only wrappers cannot execute bot turns.
# Do not pass --ignore-scripts or --omit=optional: Claude's postinstall copies
# the platform ELF over the text stub.
RUN npm install --global --omit=dev \
        "@anthropic-ai/claude-code@${CLAUDE_CLI_VERSION}" \
        "@openai/codex@${CODEX_CLI_VERSION}" \
    && python3 -c 'import pathlib,sys; p=pathlib.Path("/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe"); b=p.read_bytes()[:4]; sys.exit("claude native binary missing" if b!=b"\x7fELF" else 0)' \
    && /usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe --version | grep -F "${CLAUDE_CLI_VERSION}" \
    && npm install --global --omit=dev --ignore-scripts "@earendil-works/pi-coding-agent@${PI_CLI_VERSION}" \
    && pi --version | grep -Fx "${PI_CLI_VERSION}" \
    && npm cache clean --force

COPY images/sandbox/releases/agy-linux-*.json /tmp/
COPY images/sandbox/install-agy.py /tmp/install-agy.py
RUN python3 /tmp/install-agy.py && rm /tmp/install-agy.py /tmp/agy-linux-*.json

# Set permissions during COPY; chmod in a later layer duplicates the agent binary.
COPY --chmod=0755 --from=agent-builder /out/botbox-agent /usr/local/bin/botbox-agent
COPY --chmod=0755 images/sandbox/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY --chmod=0755 images/sandbox/install-bot-ca.sh /usr/local/bin/install-bot-ca.sh
COPY --chmod=0755 images/sandbox/chromium-launch.sh /usr/local/bin/chromium-launch.sh
# Install the launcher as its own file. COPY onto the npm symlink would
# overwrite cli.js, and the launcher would then ask Node to parse itself.
RUN mv /usr/local/bin/codex /usr/local/bin/codex-real \
    && /usr/local/bin/codex-real --version | grep -F "${CODEX_CLI_VERSION}"
COPY --chmod=0755 images/sandbox/codex-launch.sh /usr/local/bin/codex

COPY images/sandbox/claude-launch.sh /tmp/claude-launch.sh
COPY images/sandbox/install-claude-launcher.sh /tmp/install-claude-launcher.sh
RUN chmod +x /tmp/install-claude-launcher.sh \
    && /tmp/install-claude-launcher.sh /usr/local/bin/claude /tmp/claude-launch.sh \
    && rm /tmp/claude-launch.sh /tmp/install-claude-launcher.sh
COPY images/sandbox/supervisord.conf /etc/supervisor/conf.d/botbox.conf
RUN test -x /usr/local/bin/botbox-agent \
    && test -x /usr/local/bin/entrypoint.sh \
    && test -x /usr/local/bin/install-bot-ca.sh \
    && test -x /usr/local/bin/chromium-launch.sh \
    && test -x /usr/local/bin/claude

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
