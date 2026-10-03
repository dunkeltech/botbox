{ pkgs, lib, config, inputs, ... }:

{
  packages = [
    pkgs.git
    pkgs.gh
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
    DATABASE_URL = "postgres://botbox:botbox@127.0.0.1:5432/botbox?sslmode=disable";
    NATS_URL = "nats://127.0.0.1:4222";
    API_LISTEN_ADDR = "127.0.0.1:8080";
    FRONTEND_URL = "http://127.0.0.1:3000";
    PUBLIC_BASE_URL = "http://127.0.0.1:8000";
  };

  services.nats = {
    enable = true;
    jetstream.enable = true;
  };

  services.postgres = {
    enable = true;
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
    frontend.exec = "exec npm --prefix frontend run dev";
    caddy.exec = "exec caddy run --config Caddyfile --adapter caddyfile";
  };

  enterShell = ''
    echo "bot.Box dev environment ready"
    go version
  '';
}
