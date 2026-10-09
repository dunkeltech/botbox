.PHONY: dev gen lint test build sandbox-image

dev:
	devenv up

gen:
	cd backend && buf lint
	cd backend && buf generate
	cd backend && sqlc generate
	cd backend && oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml

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
	npm --prefix frontend run build

GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
SANDBOX_IMAGE_MAX_BYTES ?= 2147483648

sandbox-image:
	docker build -f images/sandbox/Dockerfile -t botbox/sandbox:dev -t botbox/sandbox:$(GIT_SHA) .
	@size=$$(docker image inspect botbox/sandbox:dev --format='{{.Size}}'); \
	echo "botbox/sandbox:dev size=$${size} bytes (budget=$(SANDBOX_IMAGE_MAX_BYTES))"; \
	test "$${size}" -le "$(SANDBOX_IMAGE_MAX_BYTES)"
