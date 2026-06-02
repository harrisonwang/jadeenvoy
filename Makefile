# JadeEnvoy Makefile
#
# Convenience targets for build / test / lint / migration.
# Run `make help` for the index.

SHELL := /bin/bash
GO    := go

# Versioning — passed to ldflags
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_TIME?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -ldflags "-s -w \
  -X github.com/harrisonwang/jadeenvoy/internal/version.Version=$(VERSION) \
  -X github.com/harrisonwang/jadeenvoy/internal/version.Commit=$(COMMIT) \
  -X github.com/harrisonwang/jadeenvoy/internal/version.BuildTime=$(BUILD_TIME)"

BIN_DIR := bin

##@ General

.PHONY: help
help: ## 显示帮助
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n\nTargets:\n"} \
	  /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } \
	  /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } \
	  ' $(MAKEFILE_LIST)

##@ Build

.PHONY: build
build: jed je je-vault ## 构建所有 binary

.PHONY: jed
jed: ## 构建 jed daemon
	@mkdir -p $(BIN_DIR)
	$(GO) build $(LDFLAGS) -o $(BIN_DIR)/jed ./cmd/jed

.PHONY: je
je: ## 构建 je CLI
	@mkdir -p $(BIN_DIR)
	$(GO) build $(LDFLAGS) -o $(BIN_DIR)/je ./cmd/je

.PHONY: je-vault
je-vault: ## 构建 je-vault MITM 代理
	@mkdir -p $(BIN_DIR)
	$(GO) build $(LDFLAGS) -o $(BIN_DIR)/je-vault ./cmd/je-vault

.PHONY: install
install: ## go install 所有 binary 到 $GOBIN
	$(GO) install $(LDFLAGS) ./cmd/jed ./cmd/je ./cmd/je-vault

##@ Test

.PHONY: test
test: ## 跑所有单元测试
	$(GO) test -race -count=1 ./...

.PHONY: test-short
test-short: ## 只跑快测试（跳过 integration）
	$(GO) test -race -short -count=1 ./...

.PHONY: test-integration
test-integration: ## 跑集成测试（需要 docker）
	$(GO) test -race -tags=integration -count=1 ./test/integration/...

.PHONY: cover
cover: ## 跑测试 + 生成覆盖率报告
	$(GO) test -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

##@ Lint / Format

.PHONY: lint
lint: ## golangci-lint check
	@command -v golangci-lint >/dev/null || (echo "Install golangci-lint first" && exit 1)
	golangci-lint run ./...

.PHONY: fmt
fmt: ## gofmt + goimports
	gofmt -s -w .
	@command -v goimports >/dev/null && goimports -w . || true

.PHONY: vet
vet: ## go vet
	$(GO) vet ./...

##@ Code generation

.PHONY: sqlc
sqlc: ## sqlc generate 生成 DB 查询代码
	@command -v sqlc >/dev/null || (echo "Install sqlc first: brew install sqlc" && exit 1)
	cd internal/store/pg && sqlc generate
	cd internal/store/sqlite && sqlc generate

.PHONY: generate
generate: sqlc ## 跑所有 codegen

##@ DB migrations

.PHONY: migrate-pg-up
migrate-pg-up: ## Postgres 跑迁移
	@command -v goose >/dev/null || (echo "Install goose first" && exit 1)
	goose -dir migrations/pg postgres "$$JE_DATABASE_URL" up

.PHONY: migrate-pg-down
migrate-pg-down: ## Postgres 回滚最近一个迁移
	goose -dir migrations/pg postgres "$$JE_DATABASE_URL" down

.PHONY: migrate-pg-status
migrate-pg-status: ## Postgres 看迁移状态
	goose -dir migrations/pg postgres "$$JE_DATABASE_URL" status

.PHONY: migrate-sqlite-up
migrate-sqlite-up: ## SQLite 跑迁移
	goose -dir migrations/sqlite sqlite3 "$$JE_DATA_DIR/oma.db" up

##@ Docker

.PHONY: docker-build
docker-build: ## 构建 docker 镜像（所有 binary）
	docker build -f docker/Dockerfile.jed -t jadeenvoy/jed:$(VERSION) .
	docker build -f docker/Dockerfile.je-vault -t jadeenvoy/je-vault:$(VERSION) .

.PHONY: docker-up
docker-up: ## docker compose 起本地栈
	docker compose -f docker/docker-compose.yml up -d --build

.PHONY: docker-down
docker-down: ## docker compose 停止 + 删除
	docker compose -f docker/docker-compose.yml down -v

.PHONY: docker-logs
docker-logs: ## 跟随 docker compose 日志
	docker compose -f docker/docker-compose.yml logs -f

##@ Dev

.PHONY: dev
dev: ## 本地起 jed（dev mode，SQLite，AUTH_MODE=bypass）
	JE_AUTH_MODE=bypass \
	JE_DATA_DIR=$(PWD)/data \
	JE_DATABASE_URL=sqlite://$(PWD)/data/jadeenvoy.db \
	$(GO) run ./cmd/jed

.PHONY: dev-real
dev-real: ## 本地起 jed（OpenAI-compatible gateway，默认 tw-agent-max；需先 export JE_LLM_API_KEY）
	test -n "$$JE_LLM_API_KEY"
	JE_AUTH_MODE=bypass \
	JE_DATA_DIR=$(PWD)/data \
	JE_DATABASE_URL=sqlite://$(PWD)/data/jadeenvoy.db \
	JE_LLM_PROVIDER=openai_compat \
	JE_LLM_BASE_URL=$${JE_LLM_BASE_URL:-http://192.168.143.117:3900/v1} \
	JE_DEFAULT_AGENT_MODEL=$${JE_DEFAULT_AGENT_MODEL:-tw-agent-max} \
	$(GO) run ./cmd/jed

.PHONY: vault-dev
vault-dev: ## 本地起 je-vault
	JE_DATA_DIR=$(PWD)/data \
	$(GO) run ./cmd/je-vault

.PHONY: clean
clean: ## 清理 build artifacts + data
	rm -rf $(BIN_DIR) dist coverage.out coverage.html
	rm -rf data

##@ Release

.PHONY: release-build
release-build: ## 跨平台构建（linux/darwin × amd64/arm64）
	@mkdir -p dist
	for OS in linux darwin; do \
	  for ARCH in amd64 arm64; do \
	    for BIN in jed je je-vault; do \
	      echo "Building $$BIN for $$OS/$$ARCH..."; \
	      CGO_ENABLED=0 GOOS=$$OS GOARCH=$$ARCH \
	        $(GO) build $(LDFLAGS) -o dist/$$BIN-$$OS-$$ARCH ./cmd/$$BIN; \
	    done; \
	  done; \
	done

.PHONY: version
version: ## 打印版本
	@echo "VERSION    = $(VERSION)"
	@echo "COMMIT     = $(COMMIT)"
	@echo "BUILD_TIME = $(BUILD_TIME)"

.DEFAULT_GOAL := help
