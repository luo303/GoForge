// MindOS 数据库迁移命令。
//
// 用法（工作目录 backend/）：
//
//	go run ./cmd/migrate up        执行所有未应用的迁移
//	go run ./cmd/migrate down      回滚最近一个迁移
//	go run ./cmd/migrate version   查看当前版本与 dirty 状态
//	go run ./cmd/migrate force N   把版本标记强制设为 N（仅用于恢复脏状态）
//
// server 不会在启动时自动迁移：自动迁移会把"部署"与"数据结构变更"耦合，
// 一次误部署就可能动到生产库。迁移显式执行，出问题（如 dirty）也显式恢复。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib" // 注册 "pgx" 这个 database/sql 驱动名

	"github.com/luo303/mindos/backend/internal/config"
	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/migrations"
)

func main() {
	// 与 server 相同的模式：run 返回 error，main 只负责打印与退出码，
	// 这样 defer 清理（关闭连接）总能执行到。
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command: up | down | version | force N")
	}

	// 迁移命令与 server 读同一份配置：.env + 环境变量，连接同一套数据库。
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	db, err := sql.Open("pgx", cfg.DB.DSN())
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer db.Close()

	// sql.Open 是惰性的，不真正连接；这里用与 server 相同的超时做一次 Ping，
	// 让"数据库没启动"这类问题在命令开头就报清楚，而不是埋进第一条迁移的错误里。
	pingCtx, cancel := context.WithTimeout(context.Background(), cfg.DB.PingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("ping postgres %s:%d/%s: %w", cfg.DB.Host, cfg.DB.Port, cfg.DB.Name, err)
	}

	runner := migrate.New(db, migrations.FS)
	ctx := context.Background()

	switch args[0] {
	case "up":
		if err := runner.Up(ctx); err != nil {
			return err
		}
		return printVersion(runner, ctx, "up complete")

	case "down":
		if err := runner.Down(ctx); err != nil {
			return err
		}
		return printVersion(runner, ctx, "down complete")

	case "version":
		return printVersion(runner, ctx, "")

	case "force":
		if len(args) != 2 {
			return fmt.Errorf("usage: migrate force <version>")
		}
		version, err := strconv.Atoi(args[1])
		if err != nil || version < 0 {
			return fmt.Errorf("force: version must be a non-negative integer, got %q", args[1])
		}
		if err := runner.Force(ctx, version); err != nil {
			return err
		}
		return printVersion(runner, ctx, "force complete")

	default:
		return fmt.Errorf("unknown command %q: up | down | version | force N", args[0])
	}
}

// printVersion 打印迁移状态。供 up/down/version/force 共用，让每条命令
// 结束时都留下"现在处于什么版本"的明确输出。
func printVersion(runner *migrate.Runner, ctx context.Context, prefix string) error {
	st, err := runner.Version(ctx)
	if err != nil {
		return err
	}
	latest, err := runner.Latest()
	if err != nil {
		return err
	}

	if prefix != "" {
		fmt.Printf("%s\n", prefix)
	}
	switch {
	case st.Dirty:
		fmt.Printf("version: %d (DIRTY — interrupted, needs recovery)\n", st.Version)
	case st.Version == 0:
		fmt.Printf("version: 0 (not initialized; latest: %d)\n", latest)
	case st.Version < latest:
		fmt.Printf("version: %d (behind; latest: %d)\n", st.Version, latest)
	default:
		fmt.Printf("version: %d (latest: %d)\n", st.Version, latest)
	}
	return nil
}
