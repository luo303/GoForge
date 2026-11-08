# Noema

在线个人知识库：上传文档，经 Agent 流水线加工为结构化笔记，支持全文检索、标签筛选与 Markdown 导出。

加工过程是**可观察、可取消、可重试、可计费**的长任务，而不是一次不可见的同步调用。

## 核心特性

- **文档接入**：Markdown / 纯文本上传，按内容 SHA-256 去重，重复提交不产生重复任务
- **流水线加工**：切分 → 抽取 → 汇总 → 打标，每步产出可追溯
- **任务治理**：并发抢占、租约接管、取消中断、失败重试、死信兜底
- **成本核算**：逐步记录 token 与费用，支持单任务预算上限
- **知识检索**：PostgreSQL 全文检索 + 标签筛选，一键导出 Markdown
- **进度可见**：状态查询与 SSE 实时推送

## 技术栈

| 层 | 选型 |
|---|---|
| 语言 | Go 1.27 |
| HTTP | Gin |
| 数据访问 | GORM（链式 API）+ pgx |
| 主库 | PostgreSQL 16（含全文检索） |
| 缓存与协调 | Redis 7 |
| 消息 | RabbitMQ 3 |
| 日志 | zap |
| 前端 | 原生 HTML / JS（M8 交付控制台） |

## 仓库结构

```text
noema/
├── backend/                 Go 服务（module: github.com/luo303/noema/backend）
│   ├── cmd/server/          进程入口：装配与启停
│   ├── internal/
│   │   ├── config/          配置加载与校验
│   │   ├── httpx/           统一响应与错误（零依赖叶子包）
│   │   ├── middleware/      RequestID / Logger / Recovery
│   │   ├── platform/        PostgreSQL 等基础设施连接
│   │   ├── handler/         协议层
│   │   ├── router/          路由表
│   │   └── logging/         日志初始化
│   └── .env.example         配置模板
├── frontend/                Web 控制台（M8 实现）
├── deployments/             PostgreSQL / Redis / RabbitMQ compose
└── docs/                    设计、计划、代码导读
```

依赖方向单向向下：`cmd/server → router → handler → service → repository → model`，`httpx` 与 `platform` 横向支撑；worker 复用 service，不经过 HTTP 层。

## 快速开始

前置：Go 1.27+、Docker。

```bash
# 1. 准备配置（只需一次；.env 已被 gitignore）
cp backend/.env.example backend/.env

# 2. 启动基础设施（在仓库根目录执行）
docker compose --env-file backend/.env -f deployments/docker-compose.yml up -d

# 3. 启动服务
cd backend
go run ./cmd/server
```

验证：

```bash
curl -i http://localhost:8080/healthz   # 存活探针，不检查依赖
curl -i http://localhost:8080/readyz    # 就绪探针，检查 PostgreSQL，不可用返回 503
```

常用命令（工作目录均为 `backend/`）：

```bash
go build ./...                  # 编译
go vet ./...                    # 静态检查
go test -race -count=1 ./...    # 测试
go fmt ./...                    # 格式化
go mod tidy                     # 整理依赖
go build -o bin/noema ./cmd/server
```

基础设施（在仓库根目录执行）：

```bash
docker compose --env-file backend/.env -f deployments/docker-compose.yml up -d
docker compose --env-file backend/.env -f deployments/docker-compose.yml ps
docker compose --env-file backend/.env -f deployments/docker-compose.yml logs -f
docker compose --env-file backend/.env -f deployments/docker-compose.yml down      # 保留数据
docker compose --env-file backend/.env -f deployments/docker-compose.yml down -v   # 清空数据
```

## 文档导航

| 文档 | 内容 |
|---|---|
| [AGENTS.md](AGENTS.md) | 开发准入约定：架构边界、硬约束、禁止事项、Git 规范 |
| [docs/design/overview.md](docs/design/overview.md) | 领域模型、状态机、API 清单、技术选型依据 |
| [docs/plan.md](docs/plan.md) | 里程碑计划：交付物、提交拆分、验证方式、退出标准 |
| [docs/guide/](docs/guide/) | 逐行代码导读 |

## 路线图

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 服务骨架与基础设施连通 | 已完成 |
| M1 | 领域模型与数据库迁移 | 进行中 |
| M2 | 文档上传与任务创建 | 待开始 |
| M3 | 加工流水线（FakeRunner） | 待开始 |
| M4 | 真实模型接入与成本核算 | 待开始 |
| M5 | 笔记检索与导出 | 待开始 |
| M6 | Redis 缓存、限流与租约 | 待开始 |
| M7 | RabbitMQ 与事件推送 | 待开始 |
| M8 | Web 控制台与工程完善 | 待开始 |

## 贡献

提交遵循 Conventional Commits，subject 与 body 使用中文，一次提交只做一件事。开始开发前请先阅读 [AGENTS.md](AGENTS.md)。
