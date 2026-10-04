FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        bash \
        curl \
        git \
        sudo \
        procps \
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
RUN mkdir -p /home/bot/.pki/nssdb /workspace \
    && certutil -N --empty-password -d sql:/home/bot/.pki/nssdb \
    && chown -R bot:bot /home/bot /workspace

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY install-bot-ca.sh /usr/local/bin/install-bot-ca.sh
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/install-bot-ca.sh

USER bot
WORKDIR /workspace

ENV HOME=/home/bot \
    DISPLAY=:0 \
    VNC_RESOLUTION=1280x800 \
    VNC_PORT=5900 \
    WEBSOCKIFY_PORT=6080

EXPOSE 6080

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
