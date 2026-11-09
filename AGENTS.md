# AGENTS.md

本文件是本仓库的**开发准入约定**，每次会话开始前必读。
它不是愿景文档：**与代码冲突时，要么改代码，要么改本文件，不允许长期不一致。**

## 1. 项目是什么

**MindOS：AI 个人知识与行动管理系统。** 文档、网页、笔记进来变成可检索的知识；对话与活动里提炼出可修订的长期记忆；Agent 拿着知识与记忆回答问题、拆解目标、调用工具执行任务。单用户、本地优先。

一句话：**让知识拥有记忆，让记忆驱动行动。** 它不是聊天套壳：知识要能回到原文、记忆要能回到来源、行动要能回到审批、Agent 每一步要能回放。

四个必须做对的工程难点：

| 业务问题 | 技术手段 | 关键机制 |
|---|---|---|
| 多格式文档要能被语义召回 | 解析 → 切分 → embedding → 向量与全文混合检索 | 幂等入库、分块策略、RRF 融合、引用定位 |
| 记忆会重复、冲突、过期 | 显式提炼 + 版本链 + 合并 | 相似度阈值、supersede 链、来源追溯 |
| Agent 不能失控、不能烧钱、要能中断恢复 | 步数 / token / 超时三重预算 + checkpoint | Eino ADK Runner、审批门、事件全量落库 |
| 长任务不能与 HTTP 生命周期绑死 | jobs 表 + RabbitMQ 派发 + 幂等消费 | 抢占领取、重试与死信、断连取消 |

**单用户是硬边界**：不建 `users` 表、不做认证与租户隔离。多用户与云端是后置阶段；提前引入 `owner_id`、登录、RLS 都算越界。

技术主线：Go + Gin + GORM + PostgreSQL 16（pgvector）+ RabbitMQ + Redis（按需）+ Eino ADK；前端 React + TypeScript + Vite + Tailwind + shadcn/ui。

**当前进度：Phase 4 已完成（2026-11-09），下一步 Phase 5 多格式与导入可靠性。** 已有迁移机制、系统状态与只读设置接口、六页前端骨架，以及 Knowledge 最小切片（Markdown/TXT 与笔记同步导入、保留码点偏移的分块、文档列表与分块预览；无向量与检索）。阶段见 `docs/plan.md`，设计见 `docs/design/overview.md`。

## 2. 仓库结构与命令

```text
mindos/
├── backend/        Go 服务（独立 module：github.com/luo303/mindos/backend）
├── frontend/       React 应用（Vite + TS + Tailwind + shadcn/ui）
├── deployments/    基础设施 compose（PostgreSQL+pgvector / RabbitMQ / Redis）
└── docs/           设计、计划
```

代码实际位于 WSL：`/home/xhh/go/src/GoForge`（物理目录名保留，不代表产品名）。
Windows 侧经 `//wsl.localhost/Ubuntu/home/xhh/go/src/GoForge` 访问。

`go` / `gofmt` / `gopls` 已全局可用（`/etc/profile.d/go.sh` 与 `/usr/local/bin` 软链），命令里不写绝对路径。
Windows 侧编辑器经 UNC 读取会有延迟，**以 WSL 内 `go build` 输出为准**；怀疑缓存陈旧时先 Reload 再判断。

**所有 Go 命令的工作目录都是 `backend/`**：

| 命令 | 用途 |
|---|---|
| `go build ./...` | 编译全部包 |
| `go vet ./...` | 静态检查 |
| `go test -race -count=1 ./...` | 单元测试（必须带 `-race`） |
| `go run ./cmd/server` | 启动服务（读取 `backend/.env`） |
| `go run ./cmd/migrate up` | 执行数据库迁移（Phase 2 起） |
| `go build -o bin/mindos ./cmd/server` | 编译可执行文件 |
| `docker compose --env-file backend/.env -f deployments/docker-compose.yml up -d` | 启动基础设施（在仓库根执行） |

前端命令（工作目录 `frontend/`）：`npm run dev` / `build` / `lint` / `test`，约定见 `frontend/README.md`。**环境归属：Docker 与数据库由项目所有者自行在 Windows 侧启动**，不代为安装，不在 WSL 内搭替代环境绕过依赖。

Go 版本：`go.mod` 声明 1.27.0，工具链 go1.27.1。不为用新特性抬高 go directive。

## 3. 模块边界（模块化单体）

按业务领域分包，不按技术层平铺；每个模块自带 model / repository / service / handler。

```text
cmd/server → router → <module>/handler → <module>/service → <module>/repository → <module>/model
                            ↘                    ↘
                          httpx               platform（PG / MQ / LLM / 存储）
agent  → 各模块 service（受控读写）      worker → 各模块 service
```

| 目录 | 只做这些 | 绝对不做 |
|---|---|---|
| `cmd/server` | 读配置、建依赖、装配、启停 | 业务逻辑、SQL |
| `internal/router` | 路由表、404/405 | 构造依赖、认识 service |
| `internal/knowledge` | 文档、分块、解析、检索 | HTTP 细节、模型调用之外的编排 |
| `internal/memory` | 提炼、去重合并、召回 | 直接改别人的表 |
| `internal/action` | 目标、计划、任务、依赖 | 直接调模型 |
| `internal/chat` | 会话、消息、SSE 推送 | 认识 eino |
| `internal/agent` | Eino 封装、工具、预算、审批、checkpoint | 认识 gin、写 HTTP 响应 |
| `internal/ctxbuild` | 上下文组装与 token 预算 | 发起模型调用 |
| `internal/insight` | 周期总结与分析 | 修改知识/记忆原文 |
| `internal/job` `internal/worker` | 任务表、派发、消费 | 写 HTTP 响应 |
| `internal/platform` | 连接、健康检查、客户端封装 | 任何业务语义 |
| `internal/httpx` | 响应结构、错误类型 | **引用项目内任何包**（叶子包） |

四条最容易破的边界：① `httpx` 不 import 任何 `internal/*`，否则 main、中间件、handler 会形成环；② 跨模块只能调对方 service 的导出方法，**不许 import 对方的 repository 与 model**；③ `agent` / `worker` 里不出现 `gin`，`chat/handler` 里不出现 `eino`；④ `platform` 里不出现 `document` / `memory` / `task` 之类的业务词。

## 4. 硬性约束（必须）

1. **单一主库**：只用 PostgreSQL（含 pgvector）。不引入独立向量数据库、不引入第二种 ORM。
2. **GORM 只用链式 API**（`db.WithContext(ctx).Where(...)`），不混用 `gorm.G[T]` 泛型 API。
3. **依赖手工装配**：全部在 `cmd/server/main.go` 用 `NewXxx()` 逐层传参，不引入 Wire / Dig / 反射容器。
4. **构造函数返回具体类型**，接口由**使用方**按需定义。
5. **对外响应只走 `httpx`**：`OK` / `Created` / `Fail` / `Abort`，不手写 `c.JSON` 拼裸 map。
6. **`context.Context` 逐层传递**：handler 传 `c.Request.Context()`，worker 传 job 级 ctx，一路到 repository 与模型调用。
7. **每个外部调用必须带超时**：DB、MQ、LLM、Embedding、文件解析、HTTP client。
8. **错误必须可追溯**：`fmt.Errorf("...: %w", err)` 或 `httpx.Error.Wrap`；不把底层错误原文返回客户端。
9. **资源必须关闭、goroutine 必须有退出路径**：连接池、`resp.Body`、`sql.Rows`、上传文件打开即 `defer Close()`；后台 goroutine 靠 `ctx.Done()` 退出并自带 `defer recover()`。
10. **Agent 三重上限**：`max_steps`、token 预算、整体超时，缺一不允许合并。
11. **工具调用必须留痕**：每次调用写 `tool_audits`；写类工具必须过审批门。
12. **阶段收尾门槛**：后端 `go build ./...` + `go vet ./...` + `go test -race ./...` 全绿；前端 `npm run lint` + `test` + `build` 全绿；该阶段端到端验证跑通。

## 5. Never（明确禁止）

**语言层面**
- **只用标准写法**：语法与第三方库照官方文档 / 官方示例的惯用姿势；不猜、不炫技、不用未文档化 API。
- 引入新依赖前确认官方 README 有可照抄示例、维护活跃；不为小功能引入重型框架。
- 不用 `io/ioutil`、不用 `interface{}`（写 `any`）、不用 GOPATH 模式。
- 没有第二个实现、也没有测试替身需求时，**不写接口**，直接用具体类型。
- 不用全局可变状态；不在 `init()` 里建连接、读配置、起 goroutine；不用 panic 做流程控制；不复制含 `sync.Mutex` 的结构体。

**数据层面**
- **成本用整数最小单位（`cost_micros`，微单位）**，token 数用 `int64`，禁止 `float64` 记账。
- 列表查询必须显式列名或说明理由，禁止模糊 `SELECT *` 进生产路径。
- 禁止 handler 里出现 SQL、事务或 `*gorm.DB`。
- 时间统一 UTC（`NowFunc` 已配好），禁止依赖本地时区。
- **禁止"先查再改"做并发控制**：任务领取必须走条件更新或行锁。
- 配置只走 `.env` + 环境变量，不引入 YAML/TOML 配置文件与 viper 类库。
- `.env.example` 里不允许出现代码尚未读取的配置项（占位即失实）。

**工程层面**
- 不提交 `.env`、真实密码、模型 API Key、加密密钥。
- 不引入 Makefile / 构建脚本 / 任务运行器，不引入微服务拆分、Service Mesh、复杂部署平台：一律 `go`、`npm`、`docker compose` 原生命令。
- 不为凑代码量增加功能；一个提交只做一个主题。
- 不在未经确认的情况下重构已完成阶段的结构。

**Agent 层面**
- 不允许"模型说完了就执行"：写操作一律生成审批请求，人确认后才落库。
- 不允许把整段对话向量化当记忆；记忆只能由提炼流程产出结构化记录。
- 不允许无上限重试模型：重试次数与预算一起判。
- 不允许 API Key 明文入库或进日志；密钥只从环境变量读，落库即加密。

## 6. 代码风格

注释密度高于常规项目：**面向首次接触 Go 的读者，读到不懂的语法就能在原地看懂。**

- 用中文；**解释 why 和机制，不只是 what**。`// 自增` 这类无信息量注释不要。
- Go 特有语法（指针接收者、方法集、接口隐式实现、`defer`、闭包、channel）就近写明：是什么、为什么这样写、与 Node/TS 的差别、易错点。
- 并发、`context`、`defer`、接口、指针语义五类代码必须写清边界条件。
- 导出标识符必须有文档注释，以标识符名开头；包级说明写在包声明上方。
- 不保留注释掉的代码；包名短小全小写无下划线；多单词文件名用下划线；错误信息小写开头、不带结尾标点。
- 中间件一律返回 `gin.HandlerFunc` 的闭包形式。
- 数据库错误统一走 `TranslateError` 翻译后的 `gorm.ErrDuplicatedKey` 等，不解析 PG 错误码字符串。
- **不维护逐行代码导读文档**：讲机制、讲取舍写进代码注释与 `docs/design/`，不另建 `docs/guide` 这类逐行讲解文件。

## 7. 响应与错误约定

```json
{ "code": "OK", "message": "success", "data": {}, "request_id": "..." }
```

- 成功 `code=OK`，HTTP 200/201；异步受理用 202。
- 失败 `code` 取预定义错误码，HTTP 状态码与语义一致。
- 预定义错误集中在 `internal/httpx/errors.go`，新增错误码必须加在这里。
- `*httpx.Error` 是模板：`WithMessage` / `Wrap` **必须返回副本**，不得修改包级变量。
- `request_id` 由中间件写入三处：`gin.Context`、标准库 `context`、响应头，缺一不可。
- SSE 接口不走信封：事件体统一 `{"type":"...","seq":n,"payload":{}}`，客户端按 `seq` 断点续传。

## 8. 新增一个接口 / 一个工具的固定流程

**接口**

1. `internal/<module>/model.go` 定义模型（显式 `TableName`、`time.Time`、金额与 token 用整数）。
2. `backend/migrations/` 写 SQL 迁移（正向 + 回滚）。
3. `internal/<module>/repository.go` 写数据访问（只收 `context.Context` + 参数）。
4. `internal/<module>/service.go` 写业务规则与事务边界。
5. `internal/<module>/handler.go` 写绑定与校验（`binding` tag），调 service，用 `httpx` 输出。
6. `internal/router/router.go` 在 `Deps` 加字段，挂到 `/api/v1`。
7. `cmd/server/main.go` 完成装配。
8. 用真实 curl 跑通成功路径 + 至少一个失败路径，贴出响应。

**工具**

1. 在 `internal/agent/tools.go` 定义工具：名称、描述、参数结构体（JSON Schema）、`risk` 等级（`read` / `write`）。
2. 只调用模块 service，不直接碰 repository。
3. 写类工具必须接审批门；读类工具必须带条数与字符上限；补一条"参数非法"测试，确认坏参数不会传给模型。

## 9. Git 约定

**一次提交只做一件事**——标准是"能否用一句 Conventional Commit 说清"。

- `feat` / `fix` / `refactor` / `test` / `docs` / `chore` 不混提。
- **重构与行为变更分开**：先 `refactor:`（测试仍全绿），再 `feat:`。
- 新功能与其单元测试可同一次提交；给其他模块补测试单独 `test:`；格式化、依赖升级、注释修正单独 `chore:`。

**信息**：格式 `type: 中文描述`，type 保留英文关键字，subject 与 body 全部中文；禁写 `update`、`fix bug`。

**提交前自检**：① `git status` 有无意外文件 ② `git diff --staged` 有无敏感信息与调试代码 ③ `go build ./...`，涉及代码再跑 `go test -race ./...`。

**禁止**：`git push`、改 remote、`--force`、`--amend` 已推送提交、`reset --hard`；危险操作先问。

## 10. 当前已知问题（待修，勿遗忘）

当前无登记未修问题。Phase 1 已关闭：panic 请求访问日志丢失、配置类型错误静默回退。新问题按“位置 / 影响 / 处理时机”追加到本节。

## 11. 阶段路线

Phase 1–3 工程基线 ｜ Phase 4–9 Knowledge ｜ Phase 10–12 Memory ｜ Phase 13–16 Agent ｜ Phase 17–18 Action ｜ Phase 19 Context ｜ Phase 20 Insights ｜ Phase 21 产品化

**跨阶段铁律：不设置工期，质量优先。每个阶段结束时可编译、可启动，且专项测试、故障验证、文档与验收记录全部完成；宁可暂停，不留半成品。**
