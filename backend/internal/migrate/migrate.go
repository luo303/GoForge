// Package migrate 提供最小可用的 SQL 迁移能力。
//
// 设计取舍：不引入 golang-migrate 这类外部迁移框架。本项目对迁移的需求只有
// 五个——按序执行、版本记录、回滚一步、dirty 中断识别、force 修正版本，用标准库
// database/sql 约 200 行即可覆盖；外部框架反而带来额外的连接管理与 dirty 语义。
// 将来若需要多来源迁移、在线 DDL 策略等高级能力，再评估替换（文件命名已与其对齐）。
//
// 崩溃安全模型（这是整个包最重要的一段注释）：
//
//	执行迁移 V 之前：先把 (V, dirty=true) 写入 schema_migrations（立即提交）
//	执行中：          迁移 SQL 跑在独立事务里
//	执行成功后：      把 (V, dirty=false) 落盘
//
// 崩溃只可能发生在三个窗口：
//  1. 标记已写、迁移没执行 → 重启后看到 dirty，知道上次没跑完；
//  2. 迁移事务中崩溃 → 事务回滚，没有半套表结构，仍是 dirty；
//  3. 迁移已提交、清标记前崩溃 → 结构其实完整，但显示 dirty，
//     用 `migrate force V` 人工确认后修正标记即可。
//
// 没有任何一种崩溃会让系统"悄悄假装正常"。
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgconn"
)

// schemaMigrationsDDL 创建版本记录表。version 0 是保留值，表示"从未迁移"。
const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    bigint      PRIMARY KEY,
    dirty      boolean     NOT NULL DEFAULT false,
    applied_at timestamptz NOT NULL DEFAULT now()
)`

// Status 是 schema_migrations 中记录的状态。
type Status struct {
	Version int  // 已应用的最新版本；0 表示从未迁移过
	Dirty   bool // true 表示上一次迁移执行到一半中断，需要人工恢复
}

// Runner 在指定的数据库上按序执行 fsys 中的迁移。
type Runner struct {
	db   *sql.DB
	fsys fs.FS
}

// New 创建迁移执行器。db 必须已完成 Ping（调用方负责），fsys 由 load 约定。
func New(db *sql.DB, fsys fs.FS) *Runner {
	return &Runner{db: db, fsys: fsys}
}

// Version 返回当前迁移状态。
//
// 有两种"什么都没有"的状态必须都当成零值 Status，而不是报错：
//  1. 表不存在（还没跑过任何迁移）；
//  2. 表存在但没有记录（ensureTable 刚建完表，第一条迁移还没执行）。
//
// 第 2 种来自 QueryRow().Scan() 的 sql.ErrNoRows——它看起来像错误，
// 实际表达的是"查不到行"。漏掉它会让空库的首次 up 直接失败：
// Up 先 ensureTable 建出空表，再调 Version，于是每次都撞在 ErrNoRows 上。
//
// 这样调用方才能区分"没迁移过"（可自动修复）与"数据库连不上"（真故障）。
// 只有数据库本身出错才返回 error。
func (r *Runner) Version(ctx context.Context) (Status, error) {
	var st Status
	row := r.db.QueryRowContext(ctx,
		`SELECT version, dirty FROM schema_migrations ORDER BY version DESC LIMIT 1`)
	switch err := row.Scan(&st.Version, &st.Dirty); {
	case err == nil:
		return st, nil
	case errors.Is(err, sql.ErrNoRows):
		// 表在、行没有：等价于"从未迁移"，版本 0 且不脏。
		return Status{}, nil
	case isRelationNotExist(err):
		return Status{}, nil
	default:
		return st, fmt.Errorf("query schema_migrations: %w", err)
	}
}

// Latest 返回 fsys 中最新的迁移版本。没有迁移文件时返回 0。
func (r *Runner) Latest() (int, error) {
	migs, err := load(r.fsys)
	if err != nil {
		return 0, err
	}
	if len(migs) == 0 {
		return 0, nil
	}
	return migs[len(migs)-1].version, nil
}

// Up 顺序执行所有版本高于当前版本的迁移。重复调用是幂等的：
// 已应用的版本会被跳过，全部最新时直接返回 nil。
func (r *Runner) Up(ctx context.Context) error {
	migs, err := load(r.fsys)
	if err != nil {
		return err
	}
	if err := r.ensureTable(ctx); err != nil {
		return err
	}
	cur, err := r.Version(ctx)
	if err != nil {
		return err
	}
	if cur.Dirty {
		return dirtyError(cur.Version)
	}
	for _, m := range migs {
		if m.version <= cur.Version {
			continue // 幂等：已应用的版本跳过
		}
		if err := r.apply(ctx, m.version, m.up); err != nil {
			return err
		}
	}
	return nil
}

// Down 回滚当前最高版本的那一个迁移。
// 只回滚一步而不是"回滚全部"：迁移回滚本身是危险操作，逐步回滚让人
// 每一步都看得见变化，出问题也更容易定位。
func (r *Runner) Down(ctx context.Context) error {
	migs, err := load(r.fsys)
	if err != nil {
		return err
	}
	if err := r.ensureTable(ctx); err != nil {
		return err
	}
	cur, err := r.Version(ctx)
	if err != nil {
		return err
	}
	if cur.Dirty {
		return dirtyError(cur.Version)
	}
	if cur.Version == 0 {
		return errors.New("no migration to rollback: database is at version 0")
	}
	var target *migration
	for i := range migs {
		if migs[i].version == cur.Version {
			target = &migs[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no down migration found for version %d in migrations dir", cur.Version)
	}
	if err := r.apply(ctx, target.version, target.down); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM schema_migrations WHERE version = $1`, target.version); err != nil {
		return fmt.Errorf("clear version %d: %w", target.version, err)
	}
	return nil
}

// Force 把版本标记直接设为 version（不清除、不执行任何 SQL）。
//
// 唯一合法的用途是恢复脏状态：确认数据库结构实际处于哪个版本后，
// 用 force 把"记录"修正到与"现实"一致。它不是日常命令，README 的恢复
// 一节会说明每一步怎么判断该 force 多少。
func (r *Runner) Force(ctx context.Context, version int) error {
	if version < 0 {
		return fmt.Errorf("version must not be negative, got %d", version)
	}
	if err := r.ensureTable(ctx); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM schema_migrations`); err != nil {
		return fmt.Errorf("clear schema_migrations: %w", err)
	}
	if version == 0 {
		return nil // 回到"从未迁移"状态
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, dirty) VALUES ($1, false)`, version); err != nil {
		return fmt.Errorf("mark version %d: %w", version, err)
	}
	return nil
}

// ensureTable 保证版本记录表存在。幂等，可重复调用。
func (r *Runner) ensureTable(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	return nil
}

// apply 执行单条迁移 SQL，并按包注释描述的崩溃安全模型维护 dirty 标记。
//
// 多语句说明：迁移文件按整体一次 Exec 下发（pgx 驱动走简单协议，
// 支持多语句），而不是按分号切割——切割会误伤函数体里的分号与美元引用。
func (r *Runner) apply(ctx context.Context, version int, sqlText string) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO schema_migrations (version, dirty) VALUES ($1, true)
		ON CONFLICT (version) DO UPDATE SET dirty = true`, version); err != nil {
		return fmt.Errorf("mark migration %d dirty: %w", version, err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		// 失败也要回滚事务，避免留下半个迁移的结构。
		// dirty 标记刻意不回滚——它就是给"下次启动"看的现场。
		_ = tx.Rollback()
		return fmt.Errorf("apply migration %d: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", version, err)
	}

	if _, err := r.db.ExecContext(ctx,
		`UPDATE schema_migrations SET dirty = false WHERE version = $1`, version); err != nil {
		return fmt.Errorf("clear dirty for migration %d: %w", version, err)
	}
	return nil
}

// dirtyError 统一 dirty 状态的报错文案，指明恢复入口。
func dirtyError(version int) error {
	return fmt.Errorf(
		"migration %d is dirty (interrupted): inspect the database, fix manually, then recover with `migrate down` or `migrate force <version>`",
		version)
}

// isRelationNotExist 判断错误是不是"表不存在"（PostgreSQL 错误码 42P01）。
// 依赖 pgconn 的具体类型是因为本项目固定使用 pgx 驱动，精确判断比匹配
// 错误字符串可靠——后者会随驱动版本变化。
func isRelationNotExist(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}
