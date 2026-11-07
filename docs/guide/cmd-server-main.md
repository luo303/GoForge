# `cmd/server/main.go` 逐行导读

> 读法：左边打开代码，右边对照本文。行号对应当前版本（131 行）。
> 本文只讲这一个文件；`config` / `platform` / `handler` 的内部实现见各自导读。

---

## 0. 先建立三个前提

**前提一：`main.go` 不是业务代码，是"装配车间"。**
它只做六件事：读配置 → 建日志 → 建根 context → 连数据库 → 装配 HTTP → 启停。任何 `if 订单状态 == ...` 之类的判断都不该出现在这里。

**前提二：Go 的 `main` 函数没有返回值，退出码只能靠 `os.Exit`。**
而 `os.Exit` 会**立刻终止进程，跳过所有 `defer`**。这是整个文件采用 `main → run() error` 结构的原因，第 3 节会详细讲。

**前提三：`gin.Engine` 本身就是一个 `http.Handler`。**
它实现了 `ServeHTTP(w, r)` 方法，所以能直接塞给 `http.Server.Handler`。框架不接管端口，端口永远由标准库 `net/http` 监听——这点和 Node 里 `app.listen(3000)` 由框架接管不一样。

---

## 1. 包声明与 import（第 1–26 行）

```go
// 第 1-4 行：文件头注释
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/luo303/GoForge/internal/config"
	...
)
```

| 行 | 内容 | 说明 |
|---|---|---|
| 1-4 | `// GoForge 服务入口。...` | 紧贴 `package` 上方的注释会成为**包文档**（`go doc` 能看到）。Go 注释就是文档，没有单独的 doc 文件 |
| 5 | `package main` | 只有 `package main` 加 `func main()` 才能编译成可执行文件。其它包名只能编译成库 |
| 7-15 | 标准库 8 个包 | `context` 传取消信号；`errors` 判错误类型；`fmt` 格式化；`net/http` HTTP 服务端；`os` 环境与退出；`os/signal` 收信号；`syscall` 信号常量；`time` 超时 |
| 17-18 | 第三方 2 个包 | `gin` Web 框架；`zap` 结构化日志 |
| 20-25 | 本项目 6 个包 | 注意路径以模块名 `github.com/luo303/GoForge` 开头，这就是 `go.mod` 里 `module` 声明的作用 |
| 分组空行 | 标准库 / 第三方 / 本项目 | 这是 Go 社区惯例（`goimports` 自动这么排）。**不是语法要求**，纯约定，但几乎所有人都这么写 |

**为什么 import 了就必须用？** Go 编译器强制：导入了不用 → 编译错误。所以你不会看到 Node 里那种 `import` 一堆只用了两个的写法。反过来，想只用副作用（如注册驱动）要写成 `import _ "xxx"`。

---

## 2. `version` 变量（第 28–29 行）

```go
// version 可以在编译时注入：go build -ldflags "-X main.version=v0.1.0"
var version = "dev"
```

| 行 | 说明 |
|---|---|
| 28 | 注释说明注入方式 |
| 29 | 包级变量。默认 `"dev"`，编译时可用 `-ldflags "-X main.version=v0.1.0"` 覆盖 |

**`-X` 的原理**：链接器把字符串变量的初始值改掉。要求变量是**包级 `string` 变量**（不能是常量、不能是函数内变量）。
**为什么不用环境变量**：版本是"编译产物"的属性，不是"运行环境"的属性；打进去以后，`/healthz` 返回的版本永远和二进制一致，不会因为部署时忘了设环境变量而错。

---

## 3. `main` 与 `run` 的分工（第 31–38 行）

```go
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "goforge: %v\n", err)
		os.Exit(1)
	}
}
```

| 行 | 说明 |
|---|---|
| 34 | `if err := run(); err != nil` —— Go 允许 if 里带初始化语句，`err` 的作用域仅限这个 if。等价于先 `err := run()` 再判断 |
| 35 | `fmt.Fprintf(os.Stderr, ...)` 写标准错误。`%v` 是"用默认格式打印任意值"，对 error 会调用它的 `Error()` |
| 36 | `os.Exit(1)`：非 0 表示失败。**它会跳过 defer**，所以放到 `main` 里、`run` 返回之后 |

**核心设计**：真正的工作在 `run()`，它返回 `error`。
好处是 `run()` 内部所有 `defer`（关数据库、刷日志）都能正常执行完毕，等它返回之后 `main` 才决定退出码。
如果直接在 `main` 里写 `os.Exit`，前面注册的 `defer` 全部失效——这是 Go 里最经典的资源泄漏来源之一。

---

## 4. 配置与日志（第 40–58 行）

```go
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log, err := logging.New(cfg.Log)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer func() { _ = log.Sync() }()

	log.Info("starting goforge",
		zap.String("version", version),
		zap.String("env", cfg.App.Env),
		zap.String("addr", cfg.App.HTTPAddr),
	)
```

| 行 | 说明 |
|---|---|
| 42 | `cfg, err := config.Load()` —— **多返回值**。Go 没有异常，错误是普通返回值，必须显式检查。`:=` 是声明并赋值（首次出现用 `:=`，之后用 `=`） |
| 43-45 | 固定套路：`if err != nil { return ... }`。这个模式在 Go 里会出现上千次，不是啰嗦，是把"可能失败"变成调用方**无法忽略**的事实 |
| 44 | `fmt.Errorf("load config: %w", err)`：`%w` 是"包装"（wrap），保留原始错误并附加上下文。之后 `errors.Is/As` 能穿透这层包装找到根因。用 `%v` 就只有字符串，链断了 |
| 48 | 日志要在配置之后、数据库之前建：后面任何一步失败都需要能打日志 |
| 52 | `defer func(){ _ = log.Sync() }()`：函数返回前把日志刷出去。终端输出无缓冲，现在几乎空转，但换成写文件后必需 |
| 54-58 | `log.Info(消息, 字段...)`：zap 的**结构化日志**。`zap.String(k, v)` 返回一个 `zap.Field`（结构体），不是拼字符串 |

**为什么用 `zap.String` 而不是 `fmt.Sprintf` 拼字符串**：结构化日志输出的是 `{"version":"dev","env":"dev"}`，日志系统（Loki/ELK）能按字段检索；拼字符串得到的是 `starting goforge version=dev env=dev`，只能全文搜索。

---

## 5. 信号与根 context（第 60–62 行）

```go
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
```

| 行 | 说明 |
|---|---|
| 61 | `signal.NotifyContext` 返回 `(ctx, stop)`。收到 `SIGINT`（Ctrl+C）或 `SIGTERM`（`docker stop` / k8s 终止）时，`ctx.Done()` 会被关闭 |
| 62 | `defer stop()` 解除信号注册，避免资源泄漏。**习惯：凡返回 cancel/stop 的函数，紧跟一行 defer** |

**`context.Background()`** 是所有 context 的根，永不过期、不可取消。这里拿它当父节点，派生出"会被信号取消"的子 context。

**和 Node 的差别**：Node 里 `process.on('SIGTERM', ...)` 监听器是全局的；Go 这里把信号变成一个 **context**，于是"取消"这件事可以沿着函数参数逐层往下传——`platform.NewPostgres(ctx, ...)` 拿到它，就能在连库卡住时随信号一起退出。

**一个可优化点**：官方文档的示例写的是 `os.Interrupt`（跨平台等价于 SIGINT），比直接用 `syscall.SIGINT` 更通用。

---

## 6. 数据库连接（第 64–73 行）

```go
	db, err := platform.NewPostgres(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := platform.ClosePostgres(db); err != nil {
			log.Warn("close postgres", zap.Error(err))
		}
	}()
```

| 行 | 说明 |
|---|---|
| 65 | 返回 `*gorm.DB`。注意它**不是一条连接**，是一个"数据库句柄 + 会话链"，内部持有 `*sql.DB` 连接池 |
| 66-68 | 这里直接 `return err` 而不加包装，因为 `NewPostgres` 内部已经写了 `open postgres: ...` / `ping postgres host:port/db: ...`，再加一层就重复了 |
| 69-73 | 关闭失败只记 warn 不返回错误：程序已经在退出路上，没有补救手段 |
| 71 | `zap.Error(err)` 是 `zap.String("error", err.Error())` 的简写形式之一 |

**为什么 `defer` 里用匿名函数**：`defer platform.ClosePostgres(db)` 也能跑，但拿不到返回值、没法记日志。包一层就能在里面判断错误。

---

## 7. HTTP 装配（第 75–95 行）

```go
	health := handler.NewHealthHandler(db, cfg.DB.PingTimeout, version)

	engine := gin.New()
	engine.Use(
		middleware.RequestID(),
		middleware.Recovery(log, cfg.IsDev()),
		middleware.Logger(log),
	)
	router.Register(engine, router.Deps{Health: health})

	srv := &http.Server{
		Addr:              cfg.App.HTTPAddr,
		Handler:           engine,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
```

| 行 | 说明 |
|---|---|
| 76 | 手工构造 handler，把依赖作为参数传进去（本项目不用 DI 容器）。`db` 传的是具体类型 `*gorm.DB` |
| 80 | `gin.New()` 返回一个**不带任何中间件**的引擎。`gin.Default()` 会自带 Logger + Recovery，我们用自己写的，所以选 `New()` |
| 81-85 | `Use` 注册全局中间件，**顺序即执行顺序**，请求按正序穿过、响应按逆序返回 |
| 86 | `router.Deps{...}` 是"依赖结构体"字面量，以后新增 handler 就加字段，不用改函数签名 |
| 88 | `&http.Server{...}` 取结构体地址，得到 `*http.Server`。Go 里所有字段都显式写，没有默认值兜底（0 值 = 不限制） |
| 90 | `Handler: engine` —— 靠的就是 `*gin.Engine` 实现了 `http.Handler` 接口 |
| 91 | `ReadHeaderTimeout`：只限制"读完请求头"的时间，防 Slowloris（慢速发头占满连接） |
| 92 | `ReadTimeout`：包含读 body，从连接建立算起 |
| 93 | `WriteTimeout`：从读完请求头开始，到写完响应为止 |
| 94 | `IdleTimeout`：keep-alive 空闲连接的存活时间 |

**这四个超时值的意义**：不设置就是"无限等待"，一个恶意或卡死的客户端能长期占住 goroutine 和连接。生产环境必须设，这是 `net/http` 文档里明确提醒的。

---

## 8. 监听与等待（第 97–118 行）

```go
	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections",
			zap.Duration("timeout", cfg.App.ShutdownTimeout))
	}
```

| 行 | 说明 |
|---|---|
| 98 | `make(chan error, 1)` 建**带缓冲 1** 的 channel。缓冲的意义见下方"为什么是 1" |
| 99 | `go func(){...}()` 启动 goroutine。`go` 关键字 + 函数调用 = 异步执行 |
| 101 | `srv.ListenAndServe()` 是**阻塞**的，直到出错或 Shutdown。它正常关闭时返回 `http.ErrServerClosed`，这不算错误，所以要排除 |
| 102 | `serverErr <- err` 把错误传回主 goroutine。`<-` 是 channel 的发送/接收符号 |
| 109-118 | `select` 同时等两个 channel，**哪个先到走哪个**。这是 Go 的"多路等待"，相当于 Promise.race，但基于 channel |
| 110 | `case err := <-serverErr:` 从 channel 收一个值 |
| 115 | `case <-ctx.Done():` context 被取消时，`Done()` 返回的 channel 会被关闭，此时接收立即返回 |

**为什么缓冲是 1**：如果 channel 无缓冲，goroutine 在 `serverErr <- nil` 处会一直阻塞，直到有人接收。而正常路径是"信号先到 → 主 goroutine 走 `ctx.Done()` 分支 → 再也不读 `serverErr`"。此时那个 goroutine 就**永久卡住**（goroutine 泄漏）。缓冲 1 让它发完即退出，不管有没有人收。

**为什么跑在 goroutine 里**：`ListenAndServe` 阻塞，如果直接调用，主 goroutine 就没法同时等信号了。所以必须"一边监听、一边等信号"。

---

## 9. 优雅关闭（第 120–131 行）

```go
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	log.Info("goforge stopped")
	return nil
```

| 行 | 说明 |
|---|---|
| 122 | **必须用 `context.Background()` 重新派生**：此刻根 `ctx` 已经被信号取消了，拿它做超时会立刻到期，等于完全不等待 |
| 123 | `defer cancel()`：释放 timer，否则会有定时器泄漏 |
| 124 | `Shutdown` 做三件事：停止接受新连接、等在途请求处理完、关闭空闲连接。超时未完成则返回错误 |
| 125 | `srv.Close()` 强制断开所有连接（包括正在处理的），是最后的兜底 |
| 129 | 走到这里说明干净退出 |

**顺序很重要**：`Shutdown` 必须在关闭数据库**之前**。因为在途请求还在用数据库，先关库会让它们全部报错。
而这个顺序是靠 `defer` 的**后进先出**保证的：`ClosePostgres` 注册得更早，所以执行得更晚。

---

## 10. 第三方库速查

| 库 | 用到的东西 | 关键认知 |
|---|---|---|
| `gin` | `gin.New()`、`engine.Use()` | `*gin.Engine` 实现了 `http.Handler`，本身不含端口监听 |
| `zap` | `log.Info(msg, zap.String(...))`、`log.Warn` | 第二个参数起是 `zap.Field`，不是格式化参数；`Named()` 生成子 logger |
| `gorm` | 只在这里用到 `*gorm.DB` 类型 | 它不是连接，是会话句柄；真正连库的参数在 `platform.NewPostgres` 里 |
| `net/http` | `http.Server`、`ListenAndServe`、`Shutdown` | 框架只管路由，监听/超时/关闭全在标准库 |

---

## 11. 自测三问

1. 如果把 `main()` 改成直接调用 `os.Exit(1)` 而不经过 `run()`，会出什么问题？
2. `serverErr` 的缓冲如果改成 0，什么情况下会泄漏一个 goroutine？
3. 为什么 `Shutdown` 要用新的 context？用根 `ctx` 会怎样？

答不上来的条目，回上面对应小节再读一遍。三题都能答，这个文件就算过关，可以进 `internal/config/config.go`。
