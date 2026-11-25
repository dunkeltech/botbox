# SPDX-License-Identifier: AGPL-3.0-only

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
# Codex includes its native runtime and tool helpers in the sandbox image.
# Pi adds its JavaScript runtime dependencies to the existing Node-based image.
SANDBOX_IMAGE_MAX_BYTES ?= 2600000000

sandbox-image:
	docker build --build-arg CLAUDE_CLI_VERSION=$${CLAUDE_CLI_VERSION:-2.1.323} --build-arg CODEX_CLI_VERSION -f images/sandbox/Dockerfile -t botbox/sandbox:dev -t botbox/sandbox:$(GIT_SHA) .
	@size=$$(docker image inspect botbox/sandbox:dev --format='{{.Size}}'); \
	echo "botbox/sandbox:dev size=$${size} bytes (budget=$(SANDBOX_IMAGE_MAX_BYTES))"; \
	test "$${size}" -le "$(SANDBOX_IMAGE_MAX_BYTES)"

.PHONY: proxy-image
proxy-image:
	docker build -f images/egress-proxy/Dockerfile -t botbox/egress-proxy:dev .

.PHONY: test-sandbox-cli
test-sandbox-cli:
	docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --user bot --entrypoint python3 --mount "type=bind,src=$(CURDIR)/images/sandbox/tests/claude_subscription_contract.py,dst=/tmp/claude_subscription_contract.py,readonly" botbox/sandbox:dev /tmp/claude_subscription_contract.py

.PHONY: test-sandbox-codex
test-sandbox-codex:
	@expected=$${CODEX_CLI_VERSION:-$$(sed -n 's/^ARG CODEX_CLI_VERSION=//p' images/sandbox/Dockerfile)}; \
	test -n "$$expected" && \
	docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --user bot --entrypoint python3 --mount "type=bind,src=$(CURDIR)/images/sandbox/tests/codex_subscription_contract.py,dst=/tmp/codex_subscription_contract.py,readonly" botbox/sandbox:dev /tmp/codex_subscription_contract.py "$$expected"

.PHONY: test-sandbox-pi
test-sandbox-pi:
	docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --user bot --entrypoint python3 --mount "type=bind,src=$(CURDIR)/images/sandbox/tests/pi_openrouter_contract.py,dst=/tmp/pi_openrouter_contract.py,readonly" botbox/sandbox:dev /tmp/pi_openrouter_contract.py

.PHONY: test-sandbox-agy
test-sandbox-agy:
	docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --user bot --entrypoint python3 --mount "type=bind,src=$(CURDIR)/images/sandbox/tests/agy_gemini_contract.py,dst=/tmp/agy_gemini_contract.py,readonly" botbox/sandbox:dev /tmp/agy_gemini_contract.py
	docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --user bot --entrypoint /usr/bin/timeout --mount "type=bind,src=$(CURDIR)/images/sandbox/tests/agy_acp_tools_contract.py,dst=/tmp/agy_acp_tools_contract.py,readonly" botbox/sandbox:dev 60s python3 /tmp/agy_acp_tools_contract.py

.PHONY: test-sandbox-chromium
test-sandbox-chromium:
	docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges --user bot --entrypoint python3 -e BOTBOX_SANDBOX_DIR=/tmp/sandbox --mount "type=bind,src=$(CURDIR)/images/sandbox,dst=/tmp/sandbox,readonly" botbox/sandbox:dev /tmp/sandbox/tests/chromium_profile_contract.py

.PHONY: codex-login
codex-login:
	cd backend && go run ./cmd/codex-login

.PHONY: claude-login
claude-login:
	docker run --rm -it --cap-drop ALL --security-opt no-new-privileges --label botbox.control-plane-login=true --entrypoint claude botbox/sandbox:dev setup-token

.PHONY: test-e2e
test-e2e:
	npm --prefix frontend run test:e2e

.PHONY: auth-broker-image test-auth-broker-sandbox
auth-broker-image:
	docker build -f images/auth-broker/Dockerfile -t botbox/auth-broker:dev .

test-auth-broker-sandbox:
	docker run --rm --init --network none --cap-drop ALL --security-opt no-new-privileges --security-opt seccomp=backend/internal/compute/docker/chromium-seccomp.json botbox/auth-broker:dev --check-sandbox
	@status=0; docker run --rm --init --network none --cap-drop ALL --security-opt no-new-privileges \
		--security-opt seccomp=backend/internal/compute/docker/chromium-seccomp.json --entrypoint /bin/sh botbox/auth-broker:dev \
		-ec 'printf "%s\n" "#!/bin/sh" "exec chromium --no-sandbox \"\$$@\"" > /tmp/chromium-test; chmod 700 /tmp/chromium-test; AUTH_BROKER_CHROMIUM_BIN=/tmp/chromium-test exec auth-broker --check-sandbox' \
		|| status=$$?; test "$$status" -eq 1
