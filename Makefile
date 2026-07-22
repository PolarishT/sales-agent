HZ ?= hz
GO_FILES := $(shell find . -type f -name '*.go' -not -path './vendor/*')

.PHONY: generate fmt test vet build run vercel-dev

generate:
	$(HZ) update --idl idl/health.thrift --sort_router
	go generate ./ent

fmt:
	gofmt -w $(GO_FILES)

test:
	go test ./...

vet:
	go vet ./...

build:
	go build -o "$${TMPDIR:-/tmp}/sales-agent-server" .

run:
	@set -a; \
	if [ -f .env.local ]; then . ./.env.local; elif [ -f .env ]; then . ./.env; fi; \
	set +a; \
	go run .

vercel-dev:
	vercel dev
