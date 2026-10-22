# GoForge —— 常用开发命令
# 带 infra-* 的命令需要本机 Docker。

APP          := goforge
CMD          := ./cmd/server
BIN_DIR      := bin
BIN          := $(BIN_DIR)/$(APP)
COMPOSE      := docker compose -f deployments/docker-compose.yml

.DEFAULT_GOAL := help

.PHONY: help
help: ## 显示可用命令
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## 整理 go.mod / go.sum
	go mod tidy

.PHONY: fmt
fmt: ## 格式化代码
	go fmt ./...

.PHONY: vet
vet: ## 静态检查
	go vet ./...

.PHONY: test
test: ## 运行单元测试（-race）
	go test -race -count=1 ./...

.PHONY: build
build: ## 编译出 bin/goforge
	go build -buildvcs=false -o $(BIN) $(CMD)

.PHONY: run
run: ## 本地直接运行（需先 make infra-up，并准备 .env）
	go run -buildvcs=false $(CMD)

.PHONY: clean
clean: ## 清理构建产物
	rm -rf $(BIN_DIR)

# ---------------- 基础设施（Docker Compose） ----------------

.PHONY: env
env: ## 从模板生成本地 .env（已存在则跳过）
	@test -f .env || (cp .env.example .env && echo "已创建 .env")
	@echo ".env ready"

.PHONY: infra-up
infra-up: ## 启动 PostgreSQL / Redis / RabbitMQ
	$(COMPOSE) up -d

.PHONY: infra-down
infra-down: ## 停止基础设施（保留数据卷）
	$(COMPOSE) down

.PHONY: infra-reset
infra-reset: ## 停止基础设施并删除数据卷（会清空数据！）
	$(COMPOSE) down -v

.PHONY: infra-logs
infra-logs: ## 查看基础设施日志
	$(COMPOSE) logs -f --tail=100

.PHONY: infra-ps
infra-ps: ## 查看基础设施状态
	$(COMPOSE) ps

# ---------------- 数据库迁移（阶段三启用） ----------------

.PHONY: migrate-up
migrate-up: ## 执行数据库迁移
	@echo "migrate-up 将在阶段三提供"

.PHONY: migrate-down
migrate-down: ## 回滚最近一次迁移
	@echo "migrate-down 将在阶段三提供"

# ---------------- Docker ----------------

.PHONY: docker-build
docker-build: ## 构建应用镜像
	docker build -t goforge:dev .

.PHONY: docker-up
docker-up: ## 用 Docker Compose 启动全部服务（含应用，阶段六启用）
	@echo "docker-up（完整栈）将在阶段六的 compose 文件中提供"
