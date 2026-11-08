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
| Knowledge | 文档/网页/笔记入库、解析、切分、向量化、混合检索、带引用问答 | Phase 4–9 |
| Memory | 四类长期记忆（画像/事实/经历/项目）的提炼、去重合并、召回 | Phase 10–12 |
| Agent | Eino ADK 运行时、工具调用、预算闸门、checkpoint、人工审批、事件回放 | Phase 13–16 |
| Action | 目标 → 计划 → 任务三级结构，依赖与进度，Agent 拆解 + 人工确认 | Phase 17–18 |
| Context | 分段 token 预算、记忆与知识编排、历史摘要压缩 | Phase 19 |
| Insights | 周期总结、学习进度、知识关联发现 | Phase 20 |

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
│   ├── cmd/migrate/         SQL 迁移执行（Phase 2）
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
│   └── migrations/          SQL 迁移（Phase 2 起）
├── frontend/                React 应用（Phase 3 起）
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
go run ./cmd/migrate up          # Phase 2 起可用
go run ./cmd/server
```

验证：

```bash
curl -i http://localhost:8080/healthz     # 存活探针，不检查依赖
curl -i http://localhost:8080/readyz      # 就绪探针，检查 PostgreSQL，不可用返回 503
```

### 常见启动问题

**配置值写错时服务不会带病启动。** 例如：

```text
mindos: load config: parse config: invalid DB_PORT: must be an integer, got "not-a-number"
```

整数、布尔值和 duration 都按严格规则解析；只有未设置或空白值才使用默认值。多个配置同时写错会一次列出，按报错中的配置项名修正 `.env` 或进程环境变量即可。

- 布尔值接受 `1/0`、`true/false`、`yes/no`、`on/off`，不接受 `maybe`。
- duration 必须带单位，例如 `10s`、`500ms`、`30m`。
- 进程环境变量优先于 `.env`，修改 `.env` 前先确认没有同名环境变量覆盖。

**请求 panic 时仍应有完整访问日志。** 日志中会同时出现：

```text
panic recovered   # 含 request_id、panic 值和堆栈
http request      # 含同一个 request_id、method、path、status=500、latency_ms
```

若只有 `panic recovered` 而没有对应 `http request`，说明中间件顺序被改坏了，应保持 `RequestID → Logger → Recovery`。前端返回统一 `INTERNAL` 信封，不暴露 panic 细节。

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
| [docs/plan.md](docs/plan.md) | Phase 1–21：范围、提交拆分、分层测试、故障验证、退出标准 |
| [docs/guide/](docs/guide/) | 逐行代码导读 |

## 路线图

| 阶段 | 内容 | 状态 |
|---|---|---|
| Phase 0 | 服务骨架（配置、日志、中间件、健康检查、优雅关闭） | 已完成 |
| Phase 1–3 | 工程基线：缺陷纠偏、迁移与系统状态、前端工程骨架 | Phase 1 已完成；P2–3 待开始 |
| Phase 4–9 | Knowledge：文本入库、多格式、异步任务、向量、混合检索、RAG | 待开始 |
| Phase 10–12 | Memory：人工管理、提炼与冲突治理、召回质量 | 待开始 |
| Phase 13–16 | Agent：最小对话、工具与预算、审批恢复、MQ 与后台执行 | 待开始 |
| Phase 17–18 | Action：任务领域、AI 计划与审批闭环 | 待开始 |
| Phase 19 | Context：分段预算、摘要压缩、长对话质量 | 待开始 |
| Phase 20 | Insights：真实统计、周期总结与学习分析 | 待开始 |
| Phase 21 | 产品化：全栈部署、端到端回归、备份恢复、长期运行 | 待开始 |

> 不设置工期。每个 Phase 的专项测试、故障验证和文档全部完成后才进入下一阶段；详细范围见 [docs/plan.md](docs/plan.md)。

## 贡献

提交遵循 Conventional Commits，subject 与 body 使用中文，一次提交只做一件事。开始开发前请先阅读 [AGENTS.md](AGENTS.md)。
