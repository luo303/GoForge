# AGENTS.md

本文件是本仓库的**开发准入约定**，每次会话开始前必读。
它不是愿景文档：**与代码冲突时，要么改代码，要么改本文件，不允许长期不一致。**

## 1. 项目是什么

GoForge 是一个**面向"有 Node.js/NestJS 经验、转 Go"的学习型后端工程**，用真实业务（同城跑腿订单调度）串起 Gin / GORM / PostgreSQL / Redis / RabbitMQ。

三个必须练到的业务难点：

| 业务问题 | 技术手段 | 练的 Go 能力 |
|---|---|---|
| 多骑手抢同一单，只能一人成功 | PG 条件更新 / 行锁 + 事务 | 事务边界、并发安全、错误处理 |
| 超时未接单自动取消退款 | Redis 过期 + RabbitMQ TTL/DLX | Goroutine、context、消费者生命周期 |
| 状态变更后可靠写流水、发通知 | Outbox + 幂等消费 | Channel、优雅关闭、失败重试 |

**当前阶段：阶段二已完成（骨架可编译、可测试），代码处于用户审查中；阶段三（模型、迁移、订单、抢单、事务）尚未开始。**

## 2. 环境与命令

代码实际位于 WSL：`/home/xhh/go/src/GoForge`；Windows 侧通过 `//wsl.localhost/Ubuntu/home/xhh/go/src/GoForge` 访问。

```bash
# WSL 内
cd /home/xhh/go/src/GoForge && go build ./...

# 从 Windows 侧代执行（go 不在非交互 shell 的 PATH，必须用绝对路径）
wsl -d Ubuntu --cd /home/xhh/go/src/GoForge -- /usr/local/go/bin/go build ./...
```

| 命令 | 用途 |
|---|---|
| `go build ./...` | 编译全部包 |
| `go vet ./...` | 静态检查 |
| `go test -race -count=1 ./...` | 单元测试（必须带 `-race`） |
| `make run` | 本地启动（需 `.env`） |
| `make infra-up` | 起 PostgreSQL / Redis / RabbitMQ |

**环境归属：Docker 与数据库由项目所有者自行在 Windows 侧启动。** 不要代为安装 PostgreSQL / Docker，也不要在 WSL 里搭替代环境来"绕过"依赖缺失。

Go 版本：`go.mod` 声明 1.27.0，工具链 go1.27.1。不要为了用新特性而抬高 go directive。

## 3. 架构边界

依赖**只允许单向向下**，反向引用即循环依赖，一律不接受：

```text
cmd/server  →  router  →  handler  →  service  →  repository  →  model
                              ↘          ↘
                             httpx      platform
```

| 包 | 只做这些 | 绝对不做 |
|---|---|---|
| `cmd/server` | 读配置、建依赖、装配、启停 | 任何业务逻辑；任何 SQL |
| `internal/router` | 路由表、404/405 | 构造依赖、认识 service |
| `internal/handler` | 绑定、校验、状态码、调 service | 写 SQL、开事务、碰 `*gorm.DB` |
| `internal/service` | 业务规则、事务边界、调 repository | 出现 `*gin.Context`、写 HTTP 响应 |
| `internal/repository` | 查询与持久化 | 业务判断、返回 `*gin.Context` |
| `internal/model` | GORM 模型与领域常量 | 依赖上面任何一层 |
| `internal/platform` | 连接、健康检查、关闭 | 任何业务语义 |
| `internal/httpx` | 响应结构、错误类型 | **引用项目内任何包**（叶子包） |

三条最容易破的边界，写死在脑子里：

1. `httpx` 不 import 任何 `internal/*`——这样 main、中间件、handler 都能引用它而不产生环。
2. `service` 里不允许出现 `gin`——否则阶段五的消费者无法复用同一套业务逻辑。
3. `platform` 里不允许出现 `order` / `rider` 之类的业务词。

---

## 4. 硬性约束（必须）

1. **单一主库**：只用 PostgreSQL。不引入 MySQL、不引入第二种 ORM。
2. **依赖手工装配**：全部在 `cmd/server/main.go` 用 `NewXxx()` 逐层传参。不引入 Wire / Dig / 反射容器。
3. **构造函数返回具体类型**（`*HealthHandler`），不要返回接口。接口由**使用方**按需定义（参考 `handler.PingFunc`）。
4. **对外响应只走 `httpx`**：`httpx.OK` / `Created` / `Fail` / `Abort`。不要手写 `c.JSON` 拼裸 map。
5. **`context.Context` 必须逐层传递**：handler 传 `c.Request.Context()`，一路传到 repository。
6. **每个外部调用必须带超时**：DB、Redis、MQ、HTTP client 都要有 `context.WithTimeout` 或客户端级 timeout。
7. **错误必须可追溯**：用 `fmt.Errorf("...: %w", err)` 或 `httpx.Error.Wrap`；不允许 `_ = err` 丢弃，也不允许把底层错误原文返回给客户端。
8. **资源必须关闭**：连接池、`resp.Body`、`sql.Rows`、文件，打开即 `defer Close()`。
9. **每个 goroutine 必须有退出路径**：靠 `ctx.Done()` / 关闭 channel / `done` 信号，并自带 `defer recover()`（panic 不跨 goroutine，没有全局兜底）。
10. **阶段收尾门槛**：`go build ./...` + `go vet ./...` + `go test -race ./...` 全绿，才允许提交并宣布阶段完成。

---

## 5. Never（明确禁止）

**语言层面**
- 不用 `io/ioutil`、不用 `interface{}`（写 `any`）、不用 GOPATH 模式。
- 不写"为了抽象而抽象"的接口：**没有第二个实现、或没有测试替身需求时，直接用具体类型。**
- 不用全局可变状态；不在 `init()` 里建连接、读配置、起 goroutine。
- 不用 panic 做流程控制；`panic` 只留给"程序无法继续"的场景。
- 不复制含 `sync.Mutex` 的结构体；不遗漏 `defer mu.Unlock()`。

**数据层面**
- **金额一律用整数分（`int64`）**，禁止 `float64` 存钱。
- 禁止 `SELECT *` 语义的模糊查询进生产路径；列表查询必须显式列名或明确说明理由。
- 禁止在 handler 里出现 SQL、事务或 `*gorm.DB`。
- 禁止依赖 `time.Now()` 的本地时区：全局统一 UTC（`NowFunc` 已配好）。
- 禁止用"先查再改"实现并发控制（抢单必须走条件更新或行锁，阶段三落实）。

**工程层面**
- 不提交 `.env`、真实密码、JWT 密钥、访问令牌（Git 操作细则见 §9）。
- 不引入微服务拆分、Service Mesh、复杂部署平台——学习项目不解决这些问题。
- 不为凑代码量增加功能；一个阶段只做一个主题。
- 不在未经确认的情况下重构已完成阶段的结构。

---

## 6. 代码风格

- **注释用中文，解释 why 不解释 what**；显而易见的代码不写注释。
- 包名短小、全小写、无下划线；多个单词的文件名用下划线分词（`request_id.go`）。
- 错误信息小写开头、不带结尾标点：`errors.New("database not initialized")`。
- 导出标识符必须有文档注释，以标识符名开头。
- 中间件返回 `gin.HandlerFunc` 的闭包形式，不定义新的中间件类型。
- 数据库错误统一走 `TranslateError` 翻译后的 `gorm.ErrDuplicatedKey` 等，**不要解析 PostgreSQL 错误码字符串**。

---

## 7. 响应与错误约定

统一结构（`httpx.Envelope`）：

```json
{ "code": "OK", "message": "success", "data": {}, "request_id": "..." }
```

- 成功：`code=OK`，HTTP 200/201。
- 失败：`code` 取预定义错误码，HTTP 状态码与语义一致（400/401/403/404/409/500/503）。
- 预定义错误在 `internal/httpx/errors.go`，**新增错误码必须加在这里**，不允许临时字符串。
- `*httpx.Error` 是模板：派生的 `WithMessage` / `Wrap` **必须返回副本**，不得修改包级变量。
- `request_id` 由 `RequestID()` 中间件写入三处：`gin.Context`、标准库 `context`、响应头。三处缺一不可。

---

## 8. 新增一个业务接口的固定流程

按顺序做，不要跳步：

1. `internal/model/` 定义模型（显式 `TableName`、时间字段用 `time.Time`、金额用 `int64`）。
2. `migrations/` 写 SQL 迁移（正向 + 回滚），并更新 `Makefile` 的 `migrate-*`。
3. `internal/repository/` 写数据访问（只收 `context.Context` + 查询参数，返回 model 或业务错误）。
4. `internal/service/` 写业务规则与事务边界（不出现 gin 类型）。
5. `internal/handler/` 写绑定与校验（`binding` tag），调 service，用 `httpx` 输出。
6. `internal/router/router.go` 在 `Deps` 里加字段，挂到 `/api/v1` 分组。
7. `cmd/server/main.go` 完成装配（repository → service → handler → Deps）。
8. 用真实 curl 跑通：成功路径 + 至少一个失败路径，贴出响应。

---

## 9. Git 约定

**一次提交只做一件事**——标准是"能否用一句 Conventional Commit 说清"，说不清就说明该拆。

- 不同性质分开：`feat` / `fix` / `refactor` / `test` / `docs` / `chore` 不混在同一次提交。
- **重构与行为变更分开**：先 `refactor:` 提交结构（测试仍全绿），再 `feat:` 提交功能；回滚功能时不该连带回滚结构。
- 新增功能与其单元测试可同一次提交；给其他模块"补测试"单独 `test:`。
- 格式化、依赖升级、注释修正这类噪音单独 `chore:`，不藏在功能提交里。
- 阶段内允许多次提交，但每次都要能独立回滚、不牵连其他改动。

**信息**：格式为 `type: 中文描述`，例如 `feat: 新增订单模型与迁移`。type 用英文关键字，其余一律中文。

**语言**：提交信息（subject 与 body）全部使用中文，不写英文句子；禁写 `update`、`fix bug` 这类无信息量描述。

**提交前自检**：① `git status` 有无意外文件（`.env` / `.tmp/` / `bin/`）② `git diff --staged` 有无敏感信息、调试代码 ③ `go build ./...`，涉及代码再跑 `go test -race ./...`。

**禁止**：`git push`、改 remote、`--force`、`--amend` 已推送的提交、`reset --hard`；危险操作先问。

---

## 10. 当前已知问题（待修，勿遗忘）

| 位置 | 问题 | 处理时机 |
|---|---|---|
| `cmd/server/main.go:81` | 中间件顺序 `RequestID → Recovery → Logger` 导致 panic 请求无访问日志（已实测：仅 1 条 `panic recovered`） | 阶段二收尾修复 |
| 仓库根 | 无 `.env`，`make run` 会因 `DB_PASSWORD is required` 直接退出 | 由环境所有者提供 |
| `.env.example` | 有 `RABBITMQ_URL`，但 `config.Config` 无对应字段 | 阶段五 |
| `config.validate` | `JWT_SECRET` 未校验，prod 下为空仍可启动 | 阶段三 |
| `config.getEnvInt` | 非法值静默回退默认值，与 `getEnvBool` 的"行为可预测"理念不一致 | 阶段三 |

---

## 11. 阶段路线

- [x] 一：项目设计
- [x] 二：初始化（配置、日志、中间件、健康检查、优雅关闭、数据库连通）
- [ ] 三：核心业务（模型、迁移、认证、订单、抢单、事务）
- [ ] 四：Redis 缓存与原子操作
- [ ] 五：RabbitMQ 异步与可靠消息
- [ ] 六：工程完善（测试、可观测性、部署）
- [ ] 七：总结与进阶

**跨阶段铁律：每个阶段结束时项目必须可编译、可启动、可验证。宁可不做，不留半成品。**
