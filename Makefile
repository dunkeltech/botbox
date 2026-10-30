.PHONY: dev gen lint test build sandbox-image

dev:
	devenv up

gen:
	cd backend && buf lint
	cd backend && buf generate
	cd backend && sqlc generate
	cd backend && oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml
	npm --prefix frontend run gen:api

lint:
	cd backend && golangci-lint run ./...
	npm --prefix frontend run lint
	npm --prefix frontend run typecheck
	npm --prefix frontend run format:check

test:
	cd backend && go test -race ./...
	npm --prefix frontend run test

build:
	mkdir -p backend/bin
	cd backend && go build -o bin/api ./cmd/api
	cd backend && go build -o bin/agent ./cmd/agent
	cd backend && go build -o bin/egress-proxy ./cmd/egress-proxy
	cd backend && go build -o bin/auth-broker ./cmd/auth-broker
	npm --prefix frontend run build

GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
# Claude Code 2.1.323 replaces the 1.0.51 JavaScript bundle with a native ELF.
# Set executable modes with COPY --chmod so later layers do not duplicate binaries.
# chromium-launch.sh and entrypoint.sh profile cleanup add ~5 KiB.
SANDBOX_IMAGE_MAX_BYTES ?= 2320795000

sandbox-image:
	docker build --build-arg CLAUDE_CLI_VERSION=$${CLAUDE_CLI_VERSION:-2.1.323} --build-arg CODEX_CLI_VERSION=$${CODEX_CLI_VERSION:-0.39.0} -f images/sandbox/Dockerfile -t botbox/sandbox:dev -t botbox/sandbox:$(GIT_SHA) .
	@size=$$(docker image inspect botbox/sandbox:dev --format='{{.Size}}'); \
	echo "botbox/sandbox:dev size=$${size} bytes (budget=$(SANDBOX_IMAGE_MAX_BYTES))"; \
	test "$${size}" -le "$(SANDBOX_IMAGE_MAX_BYTES)"

.PHONY: proxy-image
proxy-image:
	docker build -f images/egress-proxy/Dockerfile -t botbox/egress-proxy:dev .

.PHONY: test-sandbox-cli
test-sandbox-cli:
	docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --user bot --entrypoint python3 --mount "type=bind,src=$(CURDIR)/images/sandbox/tests/claude_subscription_contract.py,dst=/tmp/claude_subscription_contract.py,readonly" botbox/sandbox:dev /tmp/claude_subscription_contract.py

.PHONY: test-sandbox-chromium
test-sandbox-chromium:
	docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --user bot --entrypoint python3 -e BOTBOX_SANDBOX_DIR=/tmp/sandbox --mount "type=bind,src=$(CURDIR)/images/sandbox,dst=/tmp/sandbox,readonly" botbox/sandbox:dev /tmp/sandbox/tests/chromium_profile_contract.py

.PHONY: claude-login
claude-login:
	docker run --rm -it --cap-drop ALL --security-opt no-new-privileges --label botbox.control-plane-login=true --entrypoint claude botbox/sandbox:dev setup-token

.PHONY: test-e2e
test-e2e:
	npm --prefix frontend run test:e2e
