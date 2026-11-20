# SPDX-License-Identifier: AGPL-3.0-only

{ pkgs, lib, config, inputs, ... }:

let
  devBrokerToken = ''
    broker_token="$(
      set -eu
      broker_state="$DEVENV_STATE/auth-broker"
      umask 077
      mkdir -p "$broker_state"
      broker_token_file="$broker_state/token"
      if [ ! -s "$broker_token_file" ]; then
        broker_tmp="$(mktemp "$broker_state/token.XXXXXXXX")"
        ${pkgs.openssl}/bin/openssl rand -hex 32 > "$broker_tmp"
        # Both processes can start together. Publish only a complete token.
        if ! ln "$broker_tmp" "$broker_token_file" 2>/dev/null; then
          test -s "$broker_token_file"
        fi
        rm -f "$broker_tmp"
      fi
      cat "$broker_token_file"
    )"
  '';
in
{
  packages = [
    pkgs.git
    pkgs.gh
    pkgs.glab
    pkgs.golangci-lint
    pkgs.sqlc
    pkgs.goose
    pkgs.buf
    pkgs.protobuf
    pkgs.protoc-gen-go
    pkgs.protoc-gen-go-grpc
    pkgs.oapi-codegen
    pkgs.air
    pkgs.docker-client
    pkgs.kubectl
    pkgs.kind
    # nixpkgs names the Kubernetes package manager "kubernetes-helm" ("helm" is a synthesizer).
    pkgs.kubernetes-helm
    pkgs.caddy
    pkgs.jq
    pkgs.playwright-driver
    pkgs.playwright-driver.browsers
  ];

  languages.go.enable = true;

  languages.javascript.enable = true;
  languages.javascript.npm.enable = true;
  languages.javascript.package = pkgs.nodejs_24;

  env = {
    BOTBOX_PLAYWRIGHT_MODULE = "${pkgs.playwright-driver}";
    PLAYWRIGHT_BROWSERS_PATH = "${pkgs.playwright-driver.browsers}";
    BOTBOX_COMPUTE_DRIVER = "docker";
    # Host tools use loopback. Trusted egress workers and the auth broker
    # reach Postgres through the Docker host address.
    BOTBOX_EGRESS_DATABASE_URL = "postgres://botbox:botbox@host.docker.internal:15432/botbox?sslmode=disable";
    DATABASE_URL = "postgres://botbox:botbox@127.0.0.1:15432/botbox?sslmode=disable";
    NATS_URL = "nats://127.0.0.1:4222";
    CLAUDE_CLI_VERSION = "2.1.323";
    API_LISTEN_ADDR = "127.0.0.1:8080";
    FRONTEND_URL = "http://127.0.0.1:3000";
    PUBLIC_BASE_URL = "http://127.0.0.1:8000";
    BOTBOX_AUTH_BROKER_URL = "http://127.0.0.1:8085";
    BOTBOX_AUTH_BROKER_PROXY_URL = "http://host.docker.internal:8085";
  };

  services.nats = {
    enable = true;
    jetstream.enable = true;
  };

  services.postgres = {
    enable = true;
    listen_addresses = "127.0.0.1";
    # Leave 5432 free. devenv otherwise caches a fallback port and keeps
    # listening there after the default port is free again (cachix/devenv#2684).
    port = 15432;
    initialDatabases = [
      {
        name = "botbox";
        user = "botbox";
        pass = "botbox";
      }
    ];
  };

  processes = {
    api.exec = ''
      set -eu
      ${devBrokerToken}
      export BOTBOX_AUTH_BROKER_TOKEN="$broker_token"
      cd backend
      exec air -c .air.toml
    '';
    auth-broker.exec = ''
      set -eu
      ${devBrokerToken}
      export AUTH_BROKER_TOKEN="$broker_token"
      export DATABASE_URL="$BOTBOX_EGRESS_DATABASE_URL"
      exec docker run --rm --init --cap-drop ALL --security-opt no-new-privileges \
        --security-opt seccomp=backend/internal/compute/docker/chromium-seccomp.json \
        --publish 127.0.0.1:8085:8085 --env AUTH_BROKER_ADDR=0.0.0.0:8085 --env AUTH_BROKER_TOKEN --env DATABASE_URL botbox/auth-broker:dev
    '';
    frontend.exec = "exec npm --prefix frontend run dev";
    caddy.exec = "exec caddy run --config Caddyfile --adapter caddyfile";
  };

  enterShell = ''
    echo "bot.Box dev environment ready"
    go version
  '';
}
