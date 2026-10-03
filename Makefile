.PHONY: dev gen lint test build sandbox-image

dev:
	devenv up

gen:
	cd backend && buf lint
	cd backend && buf generate
	cd backend && sqlc generate

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

sandbox-image:
	docker build -t botbox/sandbox:dev images/sandbox
