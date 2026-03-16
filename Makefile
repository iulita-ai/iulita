.PHONY: build run clean tidy setup-hooks check-secrets ui build-go dev version release console server test test-go test-ui test-coverage docs docs-dev docs-build

APP_NAME := iulita
BUILD_DIR := bin
VERSION_PKG := github.com/iulita-ai/iulita/internal/version

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS := -s -w \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Commit=$(COMMIT) \
	-X $(VERSION_PKG).Date=$(DATE)

ui:
	cd ui && npm install && npm run build

build: ui
	go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) ./cmd/iulita/

build-go:
	go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) ./cmd/iulita/

run: build-go
	./$(BUILD_DIR)/$(APP_NAME)

server: ui
	go run -ldflags="$(LDFLAGS)" ./cmd/iulita/ --server

dev:
	cd ui && npm run dev &
	go run -ldflags="$(LDFLAGS)" ./cmd/iulita/ --server

console:
	go run -ldflags="$(LDFLAGS)" ./cmd/iulita/

version:
	@echo $(VERSION)

tidy:
	go mod tidy

clean:
	rm -rf $(BUILD_DIR)
	rm -rf ui/node_modules ui/dist

## Release

RELEASE_TAG := v0.26.0

release:
	git tag $(RELEASE_TAG)
	git push origin $(RELEASE_TAG)
	@echo "Tagged and pushed $(RELEASE_TAG)"

## Docs

docs:
	cd docs && npm ci

docs-dev:
	cd docs && npm run docs:dev

docs-build:
	cd docs && npm run docs:build

## Security

setup-hooks:
	git config core.hooksPath .githooks
	@echo "Git hooks configured (.githooks/)"
	@command -v gitleaks >/dev/null 2>&1 || { echo "Installing gitleaks..."; brew install gitleaks; }
	@echo "gitleaks installed"
	@echo "Pre-commit hook active — secrets will be blocked"

check-secrets:
	@command -v gitleaks >/dev/null 2>&1 || { echo "gitleaks not found. Run: make setup-hooks"; exit 1; }
	gitleaks detect --source . --verbose --redact

## Tests

test: test-go test-ui

test-go:
	go test ./...

test-ui:
	cd ui && npm test

test-coverage:
	go test -cover ./...
	cd ui && npm run test:coverage
