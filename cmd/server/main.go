// GoForge 服务入口。
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

	"github.com/luo303/GoForge/internal/config"
	"github.com/luo303/GoForge/internal/handler"
	"github.com/luo303/GoForge/internal/logging"
	"github.com/luo303/GoForge/internal/middleware"
	"github.com/luo303/GoForge/internal/platform"
	"github.com/luo303/GoForge/internal/router"
)

// version 可以在编译时注入：go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

func main() {
	// run 返回 error，main 只负责打印和退出码。
	// 这样所有资源清理都能用 defer 完成，而不需要在多处 os.Exit（os.Exit 不会执行 defer）。
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "goforge: %v\n", err)
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

	log.Info("starting goforge",
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

	// *gin.Engine 内部有路由表和同步保护，可以由多个 goroutine 并发读取；
	// 这里先建引擎、再交给 router 装配，避免 router 依赖太多构造细节。
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
		ReadHeaderTimeout: 5 * time.Second,  // 防止 Slowloris 类攻击
		ReadTimeout:       15 * time.Second, // 包含读 body 的时间
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// 6. 在自己的 goroutine 里启动监听，主 goroutine 负责等待信号。
	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", zap.String("addr", srv.Addr))
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

	log.Info("goforge stopped")
	return nil
}
