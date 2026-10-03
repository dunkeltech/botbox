FROM debian:bookworm-slim

# Placeholder base image for Stage 0A (`make sandbox-image`).
# Spike S1 (Stage 0B) adds minimal XFCE, VNC on :0 (1280x800), websockify, and Chromium.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates bash curl git \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
CMD ["sleep", "infinity"]
