SHELL := /bin/sh

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/kurwall/kurwall/internal/platform/version.Version=$(VERSION) \
	-X github.com/kurwall/kurwall/internal/platform/version.Commit=$(COMMIT) \
	-X github.com/kurwall/kurwall/internal/platform/version.BuildDate=$(BUILD_DATE)

.PHONY: all build web coordinator agent agent-static agent-dist proto test test-web vet fmt compose-dev compose-prod certs clean

all: build

web:
	cd web && bun install --frozen-lockfile && bun run build

coordinator:
	mkdir -p build
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o build/coordinator ./cmd/coordinator

agent:
	mkdir -p build
	CGO_ENABLED=1 go build -tags "sqlite_omit_load_extension" -trimpath -ldflags "$(LDFLAGS)" -o build/kurwall-agent ./cmd/agent

agent-static:
	./deploy/scripts/build-agent-static.sh build/kurwall-agent

agent-dist:
	VERSION="$(VERSION)" COMMIT="$(COMMIT)" BUILD_DATE="$(BUILD_DATE)" ./deploy/scripts/build-agent-dist.sh build/agent-dist

proto:
	protoc -I proto \
		--go_out=gen --go_opt=paths=source_relative \
		--go-grpc_out=gen --go-grpc_opt=paths=source_relative \
		proto/kurwall/v1/kurwall.proto

build: web coordinator agent

test:
	go test ./...

test-web:
	cd web && bun run typecheck

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal gen

certs:
	./deploy/scripts/gen-certs.sh deploy/tls

compose-dev:
	cd deploy && docker compose -f compose.dev.yml up -d --build

compose-prod:
	cd deploy && docker compose -f compose.prod.yml up -d

clean:
	rm -rf build web/dist