{ pkgs, lib, config, inputs, ... }:

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
    pkgs.playwright-driver.browsers
  ];

  languages.go.enable = true;

  languages.javascript.enable = true;
  languages.javascript.npm.enable = true;
  languages.javascript.package = pkgs.nodejs_24;

  env = {
    PLAYWRIGHT_BROWSERS_PATH = "${pkgs.playwright-driver.browsers}";
    BOTBOX_COMPUTE_DRIVER = "docker";
    # Host tools use loopback. The egress proxy runs in Docker and reaches this
    # same server through host.docker.internal. Both use 15432 so this project
    # does not take the default port other local Postgres servers use. The
    # proxy container keeps the URL it was created with.
    BOTBOX_EGRESS_DATABASE_URL = "postgres://botbox:botbox@host.docker.internal:15432/botbox?sslmode=disable";
    DATABASE_URL = "postgres://botbox:botbox@127.0.0.1:15432/botbox?sslmode=disable";
    NATS_URL = "nats://127.0.0.1:4222";
    CLAUDE_CLI_VERSION = "2.1.323";
    CODEX_CLI_VERSION = "0.39.0";
    API_LISTEN_ADDR = "127.0.0.1:8080";
    FRONTEND_URL = "http://127.0.0.1:3000";
    PUBLIC_BASE_URL = "http://127.0.0.1:8000";
    AUTH_BROKER_ADDR = "0.0.0.0:8085";
    AUTH_BROKER_TOKEN = "botbox-local-dev-auth-broker-token-01";
    BOTBOX_AUTH_BROKER_URL = "http://127.0.0.1:8085";
    BOTBOX_AUTH_BROKER_PROXY_URL = "http://host.docker.internal:8085";
    BOTBOX_AUTH_BROKER_TOKEN = "botbox-local-dev-auth-broker-token-01";
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
    api.exec = "cd backend && exec air -c .air.toml";
    auth-broker.exec = "cd backend && exec go run ./cmd/auth-broker";
    frontend.exec = "exec npm --prefix frontend run dev";
    caddy.exec = "exec caddy run --config Caddyfile --adapter caddyfile";
  };

  enterShell = ''
    echo "bot.Box dev environment ready"
    go version
  '';
}
