VERSION := $(shell sed -n 's/^MARKETING_VERSION=//p' version.env)
PREFIX ?= /usr/local

.PHONY: build test lint install
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o amazon ./cmd/amazon

test:
	TZ=UTC go test ./...

lint:
	@test -z "$$(gofmt -l cmd internal)"
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else go vet ./...; fi

install: build
	install -d "$(DESTDIR)$(PREFIX)/bin"
	install -m 0755 amazon "$(DESTDIR)$(PREFIX)/bin/amazon"
