SHELL := /bin/bash

.PHONY: generate fmt test build web-build postgres-up postgres-down dev-server dev-daemon

generate:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative api/host.proto

fmt:
	gofmt -w $$(find cmd internal gen -name '*.go' 2>/dev/null)
	npm --workspace apps/web run format --if-present

test:
	go test ./...
	npm --workspace apps/web test --if-present

build: web-build
	mkdir -p bin
	go build -o bin/agentbox-server ./cmd/agentbox-server
	go build -o bin/agentboxd ./cmd/agentboxd

web-build:
	npm --workspace apps/web run build

postgres-up:
	docker compose up -d postgres

postgres-down:
	docker compose down

dev-server:
	go run ./cmd/agentbox-server

dev-daemon:
	go run ./cmd/agentboxd
