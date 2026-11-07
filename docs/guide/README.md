# GoForge 代码导读

这套文档是给"想逐行看懂代码"的人准备的：**一个源文件对应一篇导读**，按行号或代码块解释每一行在做什么、用了什么语法、用了哪个第三方库的哪个 API。

代码里的注释解释"为什么这么写"，导读解释"这行到底在干嘛"——两者互补，不重复。

## 怎么用

1. **左手代码、右手文档**。文档里的行号对应当前版本，代码改动后行号会漂，以内容为准。
2. **先读每篇开头的"前提"小节**。那里放的是理解整个文件必需的心智模型，跳过它后面会卡。
3. **看不懂的第三方 API，先查本篇末尾的"速查表"**，那里只列真正用到的部分，不抄官方文档。
4. **每篇末尾有自测题**。答不上来就回去重读对应小节；能答上来再进下一个文件。
5. 发现文档和代码对不上，直接说——文档过期比没有文档更误导人。

## 阅读顺序

顺序按"依赖从叶子到根"排：先读不依赖项目内其它代码的，最后读组装一切的入口。

| # | 文件 | 行数 | 状态 | 这个文件真正要学的东西 |
|---|---|---|---|---|
| 1 | `cmd/server/main.go` | 131 | ✅ 已完成 | `defer` 与 `os.Exit` 的冲突、goroutine + channel 启停、`http.Server` 超时、context 取消 |
| 2 | `internal/config/config.go` | 253 | ⏳ 待写 | 多返回值、`os.LookupEnv`、`bufio.Scanner`、`errors.Join`、值/指针接收者 |
| 3 | `internal/logging/logging.go` | 43 | ⏳ 待写 | 接口组合（`zapcore.Encoder` / `Core`）、工厂函数返回 `(*T, error)` |
| 4 | `internal/platform/gorm_writer.go` | 35 | ⏳ 待写 | **隐式接口**：只实现一个 `Printf` 就满足 GORM 的 `Writer` |
| 5 | `internal/platform/gorm_level.go` | 19 | ⏳ 待写 | 枚举型返回、根据配置切换日志级别 |
| 6 | `internal/platform/postgres.go` | 93 | ⏳ 待写 | `%w` 错误链、`context.WithTimeout`、`*sql.DB` 连接池、GORM 懒连接 |
| 7 | `internal/httpx/errors.go` | 167 | ⏳ 待写 | 方法集与指针接收者、`errors.As`、包级共享实例的并发风险 |
| 8 | `internal/httpx/response.go` | 120 | ⏳ 待写 | struct tag 与反射、`omitempty` 真实行为、comma-ok 断言 |
| 9 | `internal/middleware/request_id.go` | 47 | ⏳ 待写 | 数组 vs 切片、`crypto/rand` 选型 |
| 10 | `internal/middleware/context.go` | 52 | ⏳ 待写 | 未导出空结构体做 context key、`ctx.Value` 断言 |
| 11 | `internal/middleware/request_id_mw.go` | 57 | ⏳ 待写 | 闭包工厂、`c.Request.WithContext` 必须回写 |
| 12 | `internal/middleware/logger.go` | 53 | ⏳ 待写 | 中间件何时记录、`c.Next()` 前后、`zap` 字段拼装 |
| 13 | `internal/middleware/recovery.go` | 55 | ⏳ 待写 | `defer` + `recover`、panic 不跨 goroutine |
| 14 | `internal/handler/health.go` | 94 | ⏳ 待写 | 函数类型当接口用、liveness 与 readiness 的分工 |
| 15 | `internal/router/router.go` | 54 | ⏳ 待写 | 依赖结构体、`NoRoute` / `NoMethod`、路由分组 |

## 与代码注释的分工

| 位置 | 写什么 |
|---|---|
| 源码注释 | 为什么这样设计、和 Node/TS 的差异、易错点、并发边界 |
| 本套导读 | 这一行具体在做什么、API 签名与返回值、参数含义、执行顺序 |

两者都遵守 `AGENTS.md`：中文、解释机制、不写无信息量的废话。
