GO ?= go
GOFLAGS ?=
export GOFLAGS
TESTFLAGS ?=

# Detect gotestsum
GOTESTSUM ?= $(shell command -v gotestsum 2>/dev/null)
ifdef GOTESTSUM
  TEST_CMD := $(GOTESTSUM) --format testdox --raw-command -- $(GO) test -json
else
  TEST_CMD := $(GO) test
endif

COMPOSE := docker compose -f integration/compose
COMPOSE_UP := up --build --remove-orphans --abort-on-container-exit --exit-code-from runner

# How long `make fuzz` spends on each fuzz target
FUZZTIME ?= 10s

# `make junos-schema` keeps a checkout of github.com/Juniper/yang here that
# holds two modules of every release for each of these families.  Junos OS
# Evolved comes with the junos family.
JUNOS_YANG ?= .juniper-yang
JUNOS_FAMILIES ?= junos junos-ex
# Directories with the same modules from elsewhere, such as from a device
JUNOS_YANG_MORE ?=
# The modules in conf and rpc, not their copies in the directories
# conf-with-extensions and rpc-with-extensions
JUNOS_YANG_MODULES := $(foreach family,$(JUNOS_FAMILIES),\
	'/*/*/**/conf/**/$(family)-conf-system@*.yang' '/*/*/**/rpc/**/$(family)-rpc-system@*.yang')

.PHONY: help build build-linux check test lint actionlint coverage fuzz tidy vulncheck fix clean \
	integration-test integration-netopeer2 junos-schema

##@ General
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Build
build: ## Compile all packages, including integration tests
	$(GO) build ./...
	$(GO) vet -tags=integration ./...

build-linux: ## Cross-compile for linux (amd64 and 32-bit 386)
	GOOS=linux GOARCH=amd64 $(GO) build ./...
	GOOS=linux GOARCH=386 $(GO) build ./...

##@ Quality
check: build test lint ## Run the build, unit tests, and linter

test: ## Run unit tests
	$(TEST_CMD) -race $(TESTFLAGS) ./...

lint: ## Run linter (includes vet, fmt, imports via golangci-lint)
	golangci-lint run ./...

actionlint: ## Lint the GitHub Actions workflows
	actionlint

coverage: ## Run tests with coverage
	$(TEST_CMD) -race -coverprofile=coverage.out $(TESTFLAGS) ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@$(GO) tool cover -func=coverage.out | tail -1

fuzz: ## Run every fuzz target for FUZZTIME (their seeds already run in `make test`)
	@set -e; packages=$$($(GO) list ./...); for pkg in $$packages; do \
		tests=$$($(GO) test -list '^Fuzz' "$$pkg"); \
		for target in $$(printf '%s\n' "$$tests" | awk '/^Fuzz/ {print $$1}'); do \
			echo "==> $$pkg $$target"; \
			$(GO) test -run '^$$' -fuzz "^$$target\$$" -fuzztime $(FUZZTIME) "$$pkg"; \
		done; \
	done

##@ Integration
integration-test: ## Run integration tests against the device in NETCONF_DUT_* (see integration/README.md)
	$(GO) test -tags=integration -count=1 -v -timeout 30m $(TESTFLAGS) ./integration/...

integration-netopeer2: ## Run integration tests against a containerized netopeer2 server
	$(COMPOSE)/netopeer2.yml $(COMPOSE_UP)

##@ Junos releases
junos-schema: ## Compare what the library reads with the YANG Juniper publishes for every Junos release
	@test -d $(JUNOS_YANG)/.git || git clone --quiet --depth 1 --filter=blob:none --no-checkout \
		https://github.com/Juniper/yang $(JUNOS_YANG)
	@git -C $(JUNOS_YANG) sparse-checkout set --no-cone $(JUNOS_YANG_MODULES)
	@git -C $(JUNOS_YANG) fetch --quiet --depth 1 origin HEAD
	@git -C $(JUNOS_YANG) checkout --quiet --detach FETCH_HEAD
	$(GO) run ./internal/cmd/junosschema $(JUNOS_YANG) $(JUNOS_YANG_MORE)

##@ Utility
tidy: ## Verify go.mod and go.sum are tidy
	$(GO) mod tidy -diff

vulncheck: ## Scan for known vulnerabilities
	govulncheck ./...

fix: ## Apply Go version modernizations (go fix)
	$(GO) fix ./...

clean: ## Clean build artifacts
	rm -f coverage.out coverage.html

.DEFAULT_GOAL := help
