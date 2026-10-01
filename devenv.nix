{ pkgs, lib, config, inputs, ... }:

{
      # golangci-lint for `make lint`, the linter CI runs (.github/workflows/ci.yml).
      packages = [ pkgs.git pkgs.gh pkgs.golangci-lint ];

      # Enable Go support
      languages.go.enable = true;

      # Node.js, for the frontend
      languages.javascript.enable = true;
      languages.javascript.npm.enable = true;
      languages.javascript.package = pkgs.nodejs_24;

      services.nats.enable = true;

      services.postgres = {
            enable = true;
            initialDatabases = [
                  {
                        name = "botbox";
                        user = "botbox";
                        pass = "botbox";
                  }
            ];
            initialScript = "CREATE ROLE myuser WITH LOGIN PASSWORD 'mypassword';";
      };


      # A short greeting when entering the environment
      enterShell = ''
        echo "🚀 BotBox Dev-Umgebung gestartet!"
        go version
      '';
}
