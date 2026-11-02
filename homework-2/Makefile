GO ?= go
export GOCACHE ?= /tmp/homework2-gocache
export GOMODCACHE ?= /tmp/homework2-gomodcache

.PHONY: generate build run test validate up down demo e2e

generate:
	mkdir -p internal/api
	$(GO) run -modfile=api/generator.mod -mod=mod github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config api/codegen.yaml api/openapi.yaml

build: generate
	$(GO) build -o bin/marketplace ./cmd/server

run: generate
	$(GO) run ./cmd/server

test: generate
	$(GO) test ./...

validate: generate
	$(GO) test ./internal/httpapi -run TestOpenAPI -count=1

up:
	docker compose up --build -d --wait

down:
	docker compose down

demo:
	python3 scripts/e2e.py

e2e:
	python3 scripts/e2e.py
