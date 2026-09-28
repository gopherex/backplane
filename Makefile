# backplane — platform, Go SDK, reference module `hello`, console.
#
# Conventions (shared with schemapb):
#   - ALL tools live in ./bin (version-pinned below); nothing is installed
#     globally. `git clone` + `make configure` = fully working environment.
#   - `make help` lists every target. `make check` is what a release requires.

SHELL := /bin/bash
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

BIN := $(CURDIR)/bin

# ---- pinned tool versions (bumps are explicit commits) ----------------------
EASYP_VERSION              := v0.16.6
PROTOC_GEN_GO_VERSION      := v1.36.11
PROTOC_GEN_GO_GRPC_VERSION := v1.6.2
GOLANGCI_LINT_VERSION      := v2.11.3

# easyp resolves protoc-gen-* plugins from PATH: put ./bin first.
EASYP := PATH="$(BIN):$$PATH" "$(BIN)/easyp"

# v2+ requires semantic import versioning (/v2 in the module path) — not
# supported yet; releases stay on v0/v1.
MAX_MAJOR := 1

.PHONY: help
help: ## List all targets with explanations
	@grep -hE '^[a-zA-Z0-9_.-]+:.*## ' $(MAKEFILE_LIST) | \
	  awk -F':.*## ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: configure
configure: ## Bring environment to working state: fetch all pinned tools into ./bin
	mkdir -p "$(BIN)"
	echo "--- easyp $(EASYP_VERSION)"
	GOBIN="$(BIN)" go install github.com/easyp-tech/easyp/cmd/easyp@$(EASYP_VERSION)
	echo "--- protoc-gen-go $(PROTOC_GEN_GO_VERSION)"
	GOBIN="$(BIN)" go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	echo "--- protoc-gen-go-grpc $(PROTOC_GEN_GO_GRPC_VERSION)"
	GOBIN="$(BIN)" go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	echo "--- golangci-lint $(GOLANGCI_LINT_VERSION)"
	GOBIN="$(BIN)" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	echo "--- proto deps"
	$(EASYP) mod update
	echo "✓ configure done — tools in $(BIN)"

.PHONY: gen
gen: ## Generate Go code: backplanepb and the hello example
	$(EASYP) mod download
	$(EASYP) generate
	$(EASYP) --cfg examples/hello/easyp.yaml generate

.PHONY: lint
lint: ## Lint proto files and Go code
	$(EASYP) lint -p backplanepb
	$(EASYP) --cfg examples/hello/easyp.yaml lint -p proto
	"$(BIN)/golangci-lint" run ./...

.PHONY: fmt
fmt: ## Format Go code (gofumpt, gci)
	"$(BIN)/golangci-lint" fmt ./...

.PHONY: breaking
breaking: ## Check proto files for breaking changes against master
	$(EASYP) breaking -p backplanepb

.PHONY: test
test: ## Unit tests with the race detector (integration tests skip without the stack)
	go vet ./...
	go test -race ./...

.PHONY: conformance
conformance: ## SDK contract and integration tests against platform-in-a-box (make up)
	BACKPLANE_TEST_CONSUL=localhost:8500 go test -race -count=1 ./...

.PHONY: up down
up: ## Start platform-in-a-box (docker compose)
	docker compose up -d --wait

down: ## Stop platform-in-a-box
	docker compose down

# ---- service build: stamp identity at link time --------------------------------
BUILD_PKG := github.com/gopherex/backplane/pkg/backplane/build
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0)
COMMIT    ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE      ?= $(shell date -u +%FT%TZ)
ldflags    = -X $(BUILD_PKG).Service=$(1) -X $(BUILD_PKG).Version=$(VERSION) -X $(BUILD_PKG).Commit=$(COMMIT) -X $(BUILD_PKG).Date=$(DATE)

.PHONY: hello
hello: ## Build examples/hello into ./bin/hello
	go build -trimpath -ldflags "$(call ldflags,hello)" -o "$(BIN)/hello" ./examples/hello/cmd/hello

.PHONY: run-hello
run-hello: hello ## Run hello against the local stack
	BACKPLANE_CONSUL_ADDR=localhost:8500 "$(BIN)/hello"

.PHONY: check
check: lint gen conformance ## Everything a release requires: lint, generated code is current, all tests
	git diff --exit-code --stat -- '*.pb.go' go.mod go.sum || { echo "✗ generated code or go.mod is stale — commit the result of 'make gen'"; exit 1; }

.PHONY: release
release: ## Interactive tag-driven release (runs `make check` first)
	cd "$$(git rev-parse --show-toplevel)"
	if [ -n "$$(git status --porcelain)" ]; then
	  echo "✗ Working tree is not clean — commit or stash first:"
	  git status --short
	  exit 1
	fi
	$(MAKE) check

	cur="$$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sed 's/^v//' | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)"
	cur="$${cur:-0.0.0}"
	head="$$(git rev-parse --short HEAD)"
	echo "Latest release: v$$cur    HEAD: $$head"
	echo
	echo "  1) bump version"
	echo "  2) recreate last tag (v$$cur) on HEAD   [force]"
	echo "  3) cancel"
	read -r -p "> " action

	case "$$action" in
	1)
	  IFS=. read -r MA MI PA <<< "$$cur"
	  echo
	  echo "  1) major  -> v$$((MA+1)).0.0"
	  echo "  2) minor  -> v$$MA.$$((MI+1)).0"
	  echo "  3) patch  -> v$$MA.$$MI.$$((PA+1))"
	  read -r -p "> " comp
	  case "$$comp" in
	    1) MA=$$((MA+1)); MI=0; PA=0 ;;
	    2) MI=$$((MI+1)); PA=0 ;;
	    3) PA=$$((PA+1)) ;;
	    *) echo "Aborted."; exit 0 ;;
	  esac
	  if [ "$$MA" -gt "$(MAX_MAJOR)" ]; then
	    echo "✗ v$$MA requires semantic import versioning (/v$$MA in the module path)."
	    echo "  Not supported yet — stay on v0/v1."
	    exit 1
	  fi
	  new="$$MA.$$MI.$$PA"
	  echo
	  echo "Release v$$new — will create tag v$$new on $$head and push."
	  read -r -p "Type 'yes' to proceed: " ok
	  [ "$$ok" = "yes" ] || { echo "Aborted."; exit 0; }
	  git tag -a "v$$new" -m "v$$new"
	  git push origin "v$$new"
	  echo "✓ Released v$$new."
	  ;;
	2)
	  if ! git tag -l "v$$cur" | grep -q .; then
	    echo "✗ No release tag to recreate."; exit 1
	  fi
	  echo
	  echo "Will DELETE and recreate tag v$$cur on $$head, then force-push."
	  read -r -p "Type 'yes' to proceed: " ok
	  [ "$$ok" = "yes" ] || { echo "Aborted."; exit 0; }
	  git tag -d "v$$cur"
	  git push origin ":refs/tags/v$$cur" 2>/dev/null || true
	  git tag -a "v$$cur" -m "v$$cur"
	  git push origin --force "v$$cur"
	  echo "✓ Recreated v$$cur on $$head."
	  ;;
	*) echo "Cancelled." ;;
	esac

.PHONY: clean
clean: ## Remove tools, vendored protos and generated code
	rm -rf "$(BIN)" easyp_vendor backplanepb/v1/*.pb.go
