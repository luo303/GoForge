// MindOS 服务入口。
//
// 这里的职责只有"组装"：读配置 → 建依赖 → 建 router → 启 server → 等退出信号 → 按序关闭。
// 业务代码不在这里出现，这样 entry 永远保持短小、容易阅读。
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

	"github.com/luo303/mindos/backend/internal/config"
	"github.com/luo303/mindos/backend/internal/handler"
	"github.com/luo303/mindos/backend/internal/knowledge"
	"github.com/luo303/mindos/backend/internal/logging"
	"github.com/luo303/mindos/backend/internal/middleware"
	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/platform"
	"github.com/luo303/mindos/backend/internal/router"
	"github.com/luo303/mindos/backend/migrations"
)

// version 可以在编译时注入：go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

// newEngine 装配全局中间件并返回 HTTP 引擎。
//
// 单独成函数不是为了抽象，而是为了让 main_test.go 能验证真实装配顺序：
// 如果测试自己再写一遍 Use(...)，即使 main.go 的顺序被改坏，测试也发现不了。
//
// 【为什么不用 gin.Default()】
// 它的源码就是 New() + Use(Logger(), Recovery())；内置 Logger 只往 stdout
// 打纯文本，Recovery 也不使用我们的 zap 日志与统一响应。这里照同样的标准姿势
// 展开，但换成项目自己的实现。
//
// 【中间件顺序：Logger 必须在 Recovery 外层】
// 请求表现为洋葱模型：外层中间件的前置代码先执行，内层 Handler 结束后再反向
// 执行后置代码。Logger 的访问日志写在 c.Next() 之后，必须等内层执行完才能读到
// 最终状态码与耗时。若 Recovery 在外层，Handler panic 会沿着调用栈直接跳过
// Logger 的后续代码，导致这次失败请求没有访问日志；把 Recovery 放进 Logger
// 内层，它先接住 panic 并写完 500，Logger 的 c.Next() 就能正常返回并记录日志。
func newEngine(log *zap.Logger, isDev bool) *gin.Engine {
	engine := gin.New()
	engine.Use(
		middleware.RequestID(),
		middleware.Logger(log),
		middleware.Recovery(log, isDev),
	)
	return engine
}

func main() {
	// run 返回 error，main 只负责打印和退出码。
	// 这样所有资源清理都能用 defer 完成，而不需要在多处 os.Exit（os.Exit 不会执行 defer）。
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mindos: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. 配置：失败即退出，不带着错误配置启动。
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 2. 日志：越早越好，后续所有初始化失败都要能打日志。
	log, err := logging.New(cfg.Log)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer func() { _ = log.Sync() }()

	log.Info("starting mindos",
		zap.String("version", version),
		zap.String("env", cfg.App.Env),
		zap.String("addr", cfg.App.HTTPAddr),
	)

	// 3. 根 context：收到 SIGINT/SIGTERM 时被取消，所有后台任务都挂在它下面。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 4. 基础设施连接。
	db, err := platform.NewPostgres(ctx, cfg.DB, log)
	if err != nil {
		return err // NewPostgres 已经描述了是哪个环节失败
	}
	defer func() {
		if err := platform.ClosePostgres(db); err != nil {
			log.Warn("close postgres", zap.Error(err))
		}
	}()

	// 5. HTTP 组件装配。
	health := handler.NewHealthHandler(db, cfg.DB.PingTimeout, version)

	// 迁移诊断依赖底层 *sql.DB（不是 GORM 会话）：migrate 包面向 database/sql
	// 设计，与具体 ORM 解耦。server 自己不执行迁移，只读取版本做诊断。
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB: %w", err)
	}
	runner := migrate.New(sqlDB, migrations.FS)
	if st, err := runner.Version(context.Background()); err != nil {
		log.Warn("query migration version", zap.Error(err))
	} else {
		log.Info("migration status",
			zap.Int("version", st.Version),
			zap.Bool("dirty", st.Dirty))
	}

	// 文档原文存储：只有 MinIO 一种实现，没有本地目录备选。
	// 启动时就建连并确保 bucket 存在：端点不通、凭据不对属于"部署错了"，
	// 要让服务直接起不来，而不是等用户第一次上传才看到 500。
	store, err := platform.NewMinIOStore(ctx, cfg.Storage.MinIO)
	if err != nil {
		return err
	}
	log.Info("document storage ready",
		zap.String("endpoint", store.Endpoint()),
		zap.String("bucket", store.Bucket()),
	)

	system := handler.NewSystemHandler(
		version,
		func(ctx context.Context) error {
			return platform.DBPing(ctx, cfg.DB.PingTimeout, db)
		},
		func(ctx context.Context) (bool, error) {
			return platform.VectorExtensionPresent(ctx, db)
		},
		func(ctx context.Context) (handler.MigrationInfo, error) {
			st, err := runner.Version(ctx)
			if err != nil {
				return handler.MigrationInfo{}, err
			}
			latest, err := runner.Latest()
			if err != nil {
				return handler.MigrationInfo{}, err
			}
			return handler.MigrationInfo{
				Current: st.Version,
				Latest:  latest,
				Dirty:   st.Dirty,
			}, nil
		},
		// 运行期复查 MinIO：启动成功不代表一直可用，
		// 容器被 OOM、卷被卸载、网络分区都会让它中途变坏。
		store.Health,
	)
	settings := handler.NewSettingsHandler(cfg)

	knowledgeSvc := knowledge.NewService(
		knowledge.NewRepository(db),
		store,
		cfg.Storage.MaxUploadBytes,
		log.Named("knowledge"),
	)

	engine := newEngine(log, cfg.IsDev())
	router.Register(engine, router.Deps{
		Health:    health,
		System:    system,
		Settings:  settings,
		Knowledge: knowledge.NewHandler(knowledgeSvc),
	})

	// 【为什么不用 router.Run()】
	// router.Run() 的源码只有一行：return http.ListenAndServe(address, engine)，
	// 它把 http.Server 藏起来了——没地方配超时，也没有 Shutdown 方法可用。
	// 所以直接创建标准库的 http.Server，这是 gin 官方 README
	// "Graceful shutdown or restart" 一节给出的写法。
	srv := &http.Server{
		Addr:    cfg.App.HTTPAddr,
		Handler: engine, // *gin.Engine 实现了标准库 http.Handler 接口（有 ServeHTTP 方法），
		// 所以可以直接当处理器传进来——gin 在 Go 里就是一个普通的 HTTP 处理器，不是特殊运行时。
		ReadHeaderTimeout: 5 * time.Second,  // 防止 Slowloris 类攻击
		ReadTimeout:       15 * time.Second, // 包含读 body 的时间
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// 6. 在自己的 goroutine 里启动监听，主 goroutine 负责等待信号。
	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", zap.String("addr", srv.Addr))
		// ListenAndServe 是阻塞的，正常情况下永不返回；
		// 唯一让它返回的是 Shutdown（第 8 步），此时返回 http.ErrServerClosed——
		// 这是"正常关闭"的信号，不是错误，所以用 errors.Is 排除它。
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	// 7. 等待"退出信号"或"服务异常退出"，谁先来处理谁。
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

	// 8. 优雅关闭：停止接受新连接，给在途请求留出完成时间。
	//    注意必须用独立的 context：根 ctx 已经取消了，拿它去做超时会立即返回。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close() // 超时则强制关闭，丢弃剩余连接
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	log.Info("mindos stopped")
	return nil
}
