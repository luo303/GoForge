# AGENTS.md

本文件是本仓库的**开发准入约定**，每次会话开始前必读。
它不是愿景文档：**与代码冲突时，要么改代码，要么改本文件，不允许长期不一致。**

## 1. 项目是什么

**Noema：在线个人知识库。** 用户上传文档，系统经 Agent 流水线加工（切分 → 抽取 → 汇总 → 打标）后结构化入库，支持检索与导出。

技术主线：Go + Gin + GORM + PostgreSQL + Redis + RabbitMQ。前后端同仓，后端优先。

三个必须做对的工程难点：

| 业务问题 | 技术手段 | 关键机制 |
|---|---|---|
| 同一文档不能被多个 worker 重复加工 | PG 条件更新抢占 + Redis 租约续期 | 事务边界、`RowsAffected`、行锁 |
| 长任务可取消、崩溃可恢复、失败可重试 | context 取消传播 + MQ 重试/死信 | goroutine 生命周期、幂等 |
| 用量记账与状态推送不能丢、不能重 | 事务内写流水 + Outbox + SSE | 可靠消息、缓存一致性 |

**当前进度：M0 骨架已完成（配置、日志、中间件、健康检查、优雅关闭、DB 连通）。M1 数据层未开始。**
里程碑定义见 `docs/plan.md`，领域设计见 `docs/design/overview.md`。

## 2. 仓库结构与命令

```text
noema/
├── backend/        Go 服务（独立 module：github.com/luo303/noema/backend）
├── frontend/       Web 控制台（M8 实现，当前仅占位说明）
├── deployments/    基础设施 compose（PostgreSQL / Redis / RabbitMQ）
└── docs/           设计、计划、代码导读
```

代码实际位于 WSL：`/home/xhh/go/src/GoForge`（物理目录名保留，不代表产品名）。
Windows 侧经 `//wsl.localhost/Ubuntu/home/xhh/go/src/GoForge` 访问。

**所有 Go 命令的工作目录都是 `backend/`**：

```bash
wsl -d Ubuntu --cd /home/xhh/go/src/GoForge/backend -- /usr/local/go/bin/go build ./...
```

| 命令 | 用途 |
|---|---|
| `go build ./...` | 编译全部包 |
| `go vet ./...` | 静态检查 |
| `go test -race -count=1 ./...` | 单元测试（必须带 `-race`） |
| `go run ./cmd/server` | 启动服务（读取 `backend/.env`） |
| `go build -o bin/noema ./cmd/server` | 编译可执行文件 |
| `docker compose --env-file backend/.env -f deployments/docker-compose.yml up -d` | 启动基础设施（在仓库根执行） |

**环境归属：Docker 与数据库由项目所有者自行在 Windows 侧启动。** 不代为安装，不在 WSL 内搭替代环境绕过依赖。

Go 版本：`go.mod` 声明 1.27.0，工具链 go1.27.1。不为用新特性抬高 go directive。

## 3. 架构边界

依赖**只允许单向向下**，反向引用即循环依赖：

```text
cmd/server → router → handler → service → repository → model
                          ↘         ↘
                         httpx     platform
worker（M3 起）复用 service，不经过 handler/router
```

| 包 | 只做这些 | 绝对不做 |
|---|---|---|
| `cmd/server` | 读配置、建依赖、装配、启停 | 业务逻辑、SQL |
| `internal/router` | 路由表、404/405 | 构造依赖、认识 service |
| `internal/handler` | 绑定、校验、状态码、调 service | SQL、事务、碰 `*gorm.DB` |
| `internal/service` | 业务规则、事务边界、调 repository | 出现 `*gin.Context`、写 HTTP 响应 |
| `internal/repository` | 查询与持久化 | 业务判断 |
| `internal/model` | GORM 模型与领域常量 | 依赖上层任何包 |
| `internal/runner`（M3） | 模型/工具调用抽象与实现 | 认识 gin、认识 run 状态机 |
| `internal/worker`（M3） | 抢占、执行、租约、重试 | 写 HTTP 响应 |
| `internal/platform` | 连接、健康检查、关闭 | 任何业务语义 |
| `internal/httpx` | 响应结构、错误类型 | **引用项目内任何包**（叶子包） |

三条最容易破的边界：

1. `httpx` 不 import 任何 `internal/*`，否则 main、中间件、handler 会形成环。
2. `service` 里不允许出现 `gin`，否则 worker 无法复用同一套业务逻辑。
3. `platform` 里不允许出现 `document` / `run` / `note` 之类的业务词。

## 4. 硬性约束（必须）

1. **单一主库**：只用 PostgreSQL。不引入 MySQL、不引入第二种 ORM。
2. **GORM 只用链式 API**（`db.WithContext(ctx).Where(...)`），不混用 `gorm.G[T]` 泛型 API。
3. **依赖手工装配**：全部在 `cmd/server/main.go` 用 `NewXxx()` 逐层传参，不引入 Wire / Dig / 反射容器。
4. **构造函数返回具体类型**，接口由**使用方**按需定义（参考 `handler.PingFunc`、`runner.Runner`）。
5. **对外响应只走 `httpx`**：`OK` / `Created` / `Fail` / `Abort`，不手写 `c.JSON` 拼裸 map。
6. **`context.Context` 逐层传递**：handler 传 `c.Request.Context()`，worker 传 run 级 ctx，一路到 repository 与模型调用。
7. **每个外部调用必须带超时**：DB、Redis、MQ、LLM、HTTP client 都要有 `context.WithTimeout` 或客户端级 timeout。
8. **错误必须可追溯**：`fmt.Errorf("...: %w", err)` 或 `httpx.Error.Wrap`；不允许 `_ = err` 丢弃，不把底层错误原文返回客户端。
9. **资源必须关闭**：连接池、`resp.Body`、`sql.Rows`、文件，打开即 `defer Close()`。
10. **每个 goroutine 必须有退出路径**：`ctx.Done()` / 关闭 channel / `done` 信号，并自带 `defer recover()`（panic 不跨 goroutine）。
11. **阶段收尾门槛**：`go build ./...` + `go vet ./...` + `go test -race ./...` 全绿才允许提交并宣布完成。

## 5. Never（明确禁止）

**语言层面**
- **只用标准写法**：语法与第三方库照官方文档 / 官方示例的惯用姿势；不猜、不炫技、不用未文档化 API。
- 引入新依赖前确认官方 README 有可照抄示例、维护活跃；不为小功能引入重型框架。
- 不用 `io/ioutil`、不用 `interface{}`（写 `any`）、不用 GOPATH 模式。
- 没有第二个实现、也没有测试替身需求时，**不写接口**，直接用具体类型。
- 不用全局可变状态；不在 `init()` 里建连接、读配置、起 goroutine。
- 不用 panic 做流程控制。
- 不复制含 `sync.Mutex` 的结构体；不遗漏 `defer mu.Unlock()`。

**数据层面**
- **成本一律用整数分（`int64`）**，token 数用 `int64`，禁止 `float64` 记账。
- 列表查询必须显式列名或说明理由，禁止模糊 `SELECT *` 进生产路径。
- 禁止 handler 里出现 SQL、事务或 `*gorm.DB`。
- 时间统一 UTC（`NowFunc` 已配好），禁止依赖本地时区。
- **禁止"先查再改"做并发控制**：任务抢占必须走条件更新或行锁。
- 配置只走 `.env` + 环境变量，**不引入 YAML/TOML 配置文件与 viper 类库**。
- `.env.example` 里不允许出现代码尚未读取的配置项（占位即失实）。

**工程层面**
- 不提交 `.env`、真实密码、JWT 密钥、模型 API Key。
- **不引入 Makefile / 构建脚本 / 任务运行器**：一律 `go` 与 `docker compose` 原生命令。
- 不引入微服务拆分、Service Mesh、复杂部署平台。
- 不为凑代码量增加功能；一个提交只做一个主题。
- 不在未经确认的情况下重构已完成阶段的结构。

## 6. 代码风格

注释密度高于常规项目：**面向首次接触 Go 的读者，读到不懂的语法就能在原地看懂。**

- 用中文；**解释 why 和机制，不只是 what**。`// 自增` 这类无信息量注释不要。
- Go 特有语法（指针接收者、方法集、接口隐式实现、`defer`、闭包、channel）就近写明：是什么、为什么这样写、与 Node/TS 的差别、易错点。
- 并发、`context`、`defer`、接口、指针语义五类代码必须写清边界条件。
- 导出标识符必须有文档注释，以标识符名开头；包级说明写在包声明上方。
- 不保留注释掉的代码，删掉即可。
- 包名短小全小写无下划线；多单词文件名用下划线（`request_id.go`）。
- 错误信息小写开头、不带结尾标点。
- 中间件一律返回 `gin.HandlerFunc` 的闭包形式，不定义新的中间件类型。
- 数据库错误统一走 `TranslateError` 翻译后的 `gorm.ErrDuplicatedKey` 等，不解析 PG 错误码字符串。

## 7. 响应与错误约定

```json
{ "code": "OK", "message": "success", "data": {}, "request_id": "..." }
```

- 成功 `code=OK`，HTTP 200/201；任务创建等异步受理用 202。
- 失败 `code` 取预定义错误码，HTTP 状态码与语义一致。
- 预定义错误集中在 `internal/httpx/errors.go`，新增错误码必须加在这里。
- `*httpx.Error` 是模板：`WithMessage` / `Wrap` **必须返回副本**，不得修改包级变量。
- `request_id` 由中间件写入三处：`gin.Context`、标准库 `context`、响应头，缺一不可。

## 8. 新增一个业务接口的固定流程

1. `internal/model/` 定义模型（显式 `TableName`、`time.Time`、金额与 token 用 `int64`）。
2. `migrations/` 写 SQL 迁移（正向 + 回滚），执行方式同步进 README。
3. `internal/repository/` 写数据访问（只收 `context.Context` + 参数，返回 model 或业务错误）。
4. `internal/service/` 写业务规则与事务边界。
5. `internal/handler/` 写绑定与校验（`binding` tag），调 service，用 `httpx` 输出。
6. `internal/router/router.go` 在 `Deps` 加字段，挂到 `/api/v1`。
7. `cmd/server/main.go` 完成装配。
8. 用真实 curl 跑通成功路径 + 至少一个失败路径，贴出响应。

## 9. Git 约定

**一次提交只做一件事**——标准是"能否用一句 Conventional Commit 说清"。

- `feat` / `fix` / `refactor` / `test` / `docs` / `chore` 不混提。
- **重构与行为变更分开**：先 `refactor:`（测试仍全绿），再 `feat:`。
- 新功能与其单元测试可同一次提交；给其他模块补测试单独 `test:`。
- 格式化、依赖升级、注释修正单独 `chore:`，不藏在功能提交里。
- 阶段内允许多次提交，每次都要能独立回滚。

**信息**：格式 `type: 中文描述`，type 保留英文关键字，subject 与 body 全部中文；禁写 `update`、`fix bug`。

**提交前自检**：① `git status` 有无意外文件 ② `git diff --staged` 有无敏感信息与调试代码 ③ `go build ./...`，涉及代码再跑 `go test -race ./...`。

**禁止**：`git push`、改 remote、`--force`、`--amend` 已推送提交、`reset --hard`；危险操作先问。

## 10. 当前已知问题（待修，勿遗忘）

| 位置 | 问题 | 处理时机 |
|---|---|---|
| `backend/cmd/server/main.go` | 中间件顺序 `RequestID → Recovery → Logger` 导致 panic 请求无访问日志（已实测） | M1 首个 fix 提交 |
| `backend/internal/config` | `getEnvInt` 非法值静默回退默认值，行为不可预测 | M1 |
| `backend/internal/config` | `JWT_SECRET` 未做启动校验，prod 下为空仍可启动 | M8 认证落地时 |
| `docs/guide/cmd-server-main.md` | 行号基于早期版本，后续以代码块为准 | 随导读更新 |

## 11. 里程碑

M0 ✅ 骨架 ｜ M1 数据层与迁移 ｜ M2 文档上传与任务创建 ｜ M3 加工流水线（FakeRunner）｜ M4 真实模型接入与记账 ｜ M5 笔记检索 ｜ M6 Redis ｜ M7 RabbitMQ 与事件推送 ｜ M8 控制台与工程完善

**跨阶段铁律：每个阶段结束时项目必须可编译、可启动、可验证。宁可不做，不留半成品。**
