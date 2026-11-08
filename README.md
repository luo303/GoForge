# MindOS

**AI-Powered Personal Knowledge & Action Workspace**

> 让知识拥有记忆，让记忆驱动行动。

MindOS 是一个单用户、本地优先的 AI 个人工作空间：文档、网页、笔记进来变成可检索的知识；对话与活动中提炼出可修订的长期记忆；Agent 拿着知识与记忆回答问题、拆解目标、调用工具执行任务。

它不是聊天工具的壳，区别在于三件事必须成立：

- **知识可追溯**：每句回答都能落到具体文档片段，点开就是原文位置。
- **记忆可修订**：记忆有版本、有来源、能合并、能作废，不是只增不改的碎碎念。
- **行动可确认**：Agent 的写操作先出审批单，人点了才落库，每次工具调用都有审计。

## 能力概览

| 模块 | 做什么 | 阶段 |
|---|---|---|
| Knowledge | 文档/网页/笔记入库、解析、切分、向量化、混合检索、带引用问答 | Phase 2 |
| Memory | 四类长期记忆（画像/事实/经历/项目）的提炼、去重合并、召回 | Phase 3 |
| Agent | Eino ADK 运行时、工具调用、预算闸门、checkpoint、人工审批、事件回放 | Phase 4 |
| Action | 目标 → 计划 → 任务三级结构，依赖与进度，Agent 拆解 + 人工确认 | Phase 5 |
| Context | 分段 token 预算、记忆与知识编排、历史摘要压缩 | Phase 6 |
| Insights | 周期总结、学习进度、知识关联发现 | Phase 7 |

## 技术栈

| 层 | 选型 |
|---|---|
| 后端 | Go 1.27 + Gin |
| 数据访问 | GORM（链式 API）+ pgx |
| 主库 | PostgreSQL 16 + pgvector（业务数据、向量、全文检索同库） |
| 异步 | RabbitMQ（派发）+ `jobs` 表（状态真相） |
| Agent | Eino / Eino ADK（ReAct、checkpoint、interrupt/resume） |
| 实时 | SSE（流式回答与执行轨迹） |
| 模型 | OpenAI 兼容 API，LLM 与 Embedding 均可替换 |
| 前端 | React + TypeScript + Vite + Tailwind CSS + shadcn/ui |
| 部署 | Docker + Docker Compose（本地单机） |

## 仓库结构

```text
mindos/
├── backend/                 Go 服务（module: github.com/luo303/mindos/backend）
│   ├── cmd/server/          进程入口：装配与启停
│   ├── cmd/migrate/         SQL 迁移执行（Phase 1）
│   ├── internal/
│   │   ├── config/          配置加载与校验
│   │   ├── logging/         zap 初始化
│   │   ├── httpx/           统一响应与错误（零依赖叶子包）
│   │   ├── middleware/      RequestID / Logger / Recovery
│   │   ├── platform/        PostgreSQL、文件存储、MQ、模型客户端
│   │   ├── knowledge/       文档、解析、切分、检索
│   │   ├── memory/          记忆提炼、合并、召回
│   │   ├── agent/           Eino 封装、工具、预算、审批、审计
│   │   ├── chat/            会话、消息、SSE
│   │   ├── action/          目标、计划、任务
│   │   ├── ctxbuild/        上下文组装与 token 预算
│   │   ├── insight/         周期总结与分析
│   │   ├── job/ worker/     异步任务与消费
│   │   ├── handler/ router/ 协议层与路由表
│   └── migrations/          SQL 迁移（Phase 1 起）
├── frontend/                React 应用（Phase 1 起）
├── deployments/             PostgreSQL(pgvector) / RabbitMQ / Redis compose
└── docs/                    设计、计划、代码导读
```

依赖单向：`router → <module>/handler → <module>/service → <module>/repository → <module>/model`；`agent` 与 `worker` 复用模块 service，不经过 HTTP 层。当前代码只到 Phase 0 骨架，模块目录随阶段落地。

## 快速开始

前置：Go 1.27+、Node 20+、Docker。

```bash
# 1. 准备配置（只需一次；.env 已被 gitignore）
cp backend/.env.example backend/.env

# 2. 启动基础设施（在仓库根目录执行）
docker compose --env-file backend/.env -f deployments/docker-compose.yml up -d

# 3. 执行迁移并启动服务
cd backend
go run ./cmd/migrate up          # Phase 1 起可用
go run ./cmd/server
```

验证：

```bash
curl -i http://localhost:8080/healthz     # 存活探针，不检查依赖
curl -i http://localhost:8080/readyz      # 就绪探针，检查 PostgreSQL，不可用返回 503
```

常用命令（工作目录 `backend/`）：

```bash
go build ./...                  # 编译
go vet ./...                    # 静态检查
go test -race -count=1 ./...    # 测试
go fmt ./...                    # 格式化
go mod tidy                     # 整理依赖
```

基础设施（在仓库根目录执行）：

```bash
docker compose --env-file backend/.env -f deployments/docker-compose.yml up -d
docker compose --env-file backend/.env -f deployments/docker-compose.yml ps
docker compose --env-file backend/.env -f deployments/docker-compose.yml logs -f
docker compose --env-file backend/.env -f deployments/docker-compose.yml down      # 保留数据
docker compose --env-file backend/.env -f deployments/docker-compose.yml down -v   # 清空数据
```

> PostgreSQL 镜像为 `pgvector/pgvector:pg16`。从旧版本切过来时数据卷不兼容，需要 `down -v` 后重建（当前阶段库内无业务数据）。

## 文档导航

| 文档 | 内容 |
|---|---|
| [AGENTS.md](AGENTS.md) | 开发准入约定：模块边界、硬约束、禁止事项、Git 规范 |
| [docs/design/overview.md](docs/design/overview.md) | 领域模型、检索与记忆机制、Agent 运行时、API 清单 |
| [docs/plan.md](docs/plan.md) | Phase 1–8：交付物、提交拆分、验证方式、退出标准 |
| [docs/guide/](docs/guide/) | 逐行代码导读 |

## 路线图

| 阶段 | 内容 | 状态 |
|---|---|---|
| Phase 0 | 服务骨架（配置、日志、中间件、健康检查、优雅关闭） | 已完成 |
| Phase 1 | 工程与基础设施：迁移、pgvector、配置、前端脚手架 | 待开始 |
| Phase 2 | Knowledge：入库、切分、向量化、混合检索、RAG | 待开始 |
| Phase 3 | Memory：提炼、合并、召回、管理 | 待开始 |
| Phase 4 | Agent：运行时、工具、预算、checkpoint、审批、SSE | 待开始 |
| Phase 5 | Action：目标、计划、任务、人工确认 | 待开始 |
| Phase 6 | Context：分段预算、摘要压缩 | 待开始 |
| Phase 7 | Insights：周期总结与学习分析 | 待开始 |
| Phase 8 | 产品完善：测试、监控、部署、文档 | 待开始 |

## 贡献

提交遵循 Conventional Commits，subject 与 body 使用中文，一次提交只做一件事。开始开发前请先阅读 [AGENTS.md](AGENTS.md)。
