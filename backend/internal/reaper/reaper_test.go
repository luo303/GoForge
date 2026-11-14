package reaper

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/migrate"
	"github.com/luo303/mindos/backend/internal/outbox"
	"github.com/luo303/mindos/backend/internal/task"
	"github.com/luo303/mindos/backend/migrations"
)

// 本文件是真实 PostgreSQL 集成测试：兜底扫描的所有判断都依赖
// "tasks 与 outbox 的联合状态"，用替身测不出真实的 NOT EXISTS 与时间比较。
//
// 运行方式（先启动 compose）：
//
//	MINDOS_TEST_DSN="host=localhost port=5432 user=mindos password=mindos_dev_password dbname=postgres sslmode=disable" \
//	go test -count=1 -run Integration ./internal/reaper/
const testDSNEnv = "MINDOS_TEST_DSN"

func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	adminDSN := os.Getenv(testDSNEnv)
	if adminDSN == "" {
		t.Skipf("%s not set; skipping integration test", testDSNEnv)
	}

	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	defer admin.Close()

	testDB := fmt.Sprintf("mindos_reaper_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(fmt.Sprintf("CREATE DATABASE %s", testDB)); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", testDB))
	})

	pgCfg, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		t.Fatalf("parse %s: %v", testDSNEnv, err)
	}
	pgCfg.Database = testDB
	db, err := sql.Open("pgx", stdlib.RegisterConnConfig(pgCfg))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := migrate.New(db, migrations.FS).Up(ctx); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{
		SkipDefaultTransaction: true,
		TranslateError:         true,
		NowFunc:                func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("open gorm: %v", err)
	}
	return gdb
}

// newReaper 用很短的扫描参数构造，避免测试里 sleep 太久。
func newReaper(db *gorm.DB, staleAfter time.Duration) *Reaper {
	return New(db, task.NewRepository(db), outbox.NewRepository(db), nil, Options{
		Interval:   time.Hour, // 测试直接调 once，不靠 ticker
		StaleAfter: staleAfter,
		Retention:  time.Hour,
		Batch:      50,
	})
}

// insertTask 造一条任务，并把 updated_at 拉到过去（模拟"很久没动"）。
func insertTask(t *testing.T, db *gorm.DB, kind string, docID int64, status task.Status, idle time.Duration) *task.Task {
	t.Helper()
	ctx := context.Background()
	tt := task.New(kind, docID)
	if err := task.AppendTx(ctx, db.WithContext(ctx), tt); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if err := db.Exec("UPDATE tasks SET status = ?, updated_at = now() - make_interval(secs => ?) WHERE id = ?",
		status, idle.Seconds(), tt.ID).Error; err != nil {
		t.Fatalf("age task: %v", err)
	}
	return tt
}

func countRows(t *testing.T, db *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// 卡住的任务要被重新投一条消息，并且任务状态回到 pending——
// 否则消费端的 MarkRunning（只接受 pending）会直接跳过它，投了也没用。
func TestReaperRequeuesStuckTaskIntegration(t *testing.T) {
	db := setupDB(t)
	r := newReaper(db, 2*time.Minute)

	// running + 5 分钟没动：典型的"消费端拿到消息后崩了"。
	tt := insertTask(t, db, "document.import", 42, task.StatusRunning, 5*time.Minute)

	r.once(context.Background())

	var status task.Status
	if err := db.Raw("SELECT status FROM tasks WHERE id = ?", tt.ID).Scan(&status).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if status != task.StatusPending {
		t.Errorf("status = %s, want pending after requeue", status)
	}

	var msgKey, topic string
	if err := db.Raw("SELECT msg_key, topic FROM outbox ORDER BY id DESC LIMIT 1").Row().Scan(&msgKey, &topic); err != nil {
		t.Fatalf("load outbox: %v", err)
	}
	if msgKey != "42" || topic != "document.import" {
		t.Errorf("requeued message = %s/%s, want 42/document.import", msgKey, topic)
	}
}

// 还在正常处理窗口内的任务不能被当成僵死：抢在前面重投会白占一次执行名额。
func TestReaperLeavesFreshTaskAloneIntegration(t *testing.T) {
	db := setupDB(t)
	r := newReaper(db, 2*time.Minute)

	insertTask(t, db, "document.import", 43, task.StatusRunning, 10*time.Second)
	r.once(context.Background())

	if n := countRows(t, db, "SELECT count(*) FROM outbox"); n != 0 {
		t.Errorf("outbox rows = %d, want 0 for a fresh task", n)
	}
}

// 已经有待发消息时不能再投一条：relay 马上就发，重复投递会让同一任务跑两次。
func TestReaperSkipsTaskWithPendingMessageIntegration(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	r := newReaper(db, 2*time.Minute)

	tt := insertTask(t, db, "document.import", 44, task.StatusPending, 10*time.Minute)
	msg := outbox.New("document.import", "44", map[string]any{"document_id": int64(44), "task_id": tt.ID})
	if err := outbox.NewRepository(db).Append(ctx, msg); err != nil {
		t.Fatalf("append message: %v", err)
	}

	r.once(ctx)

	// 还是那一条，没有新增。
	if n := countRows(t, db, "SELECT count(*) FROM outbox"); n != 1 {
		t.Errorf("outbox rows = %d, want 1", n)
	}
}

// 清理只动已投递且过期的：待发与"投递失败等人看"的都不能被删掉。
func TestReaperPurgeOnlyTouchesSentIntegration(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	repo := outbox.NewRepository(db)
	r := newReaper(db, 2*time.Minute)

	mk := func(status outbox.Status, publishedAgo time.Duration) *outbox.Message {
		m := outbox.New("document.import", "1", map[string]any{"document_id": int64(1)})
		m.Status = status
		if err := repo.Append(ctx, m); err != nil {
			t.Fatalf("append: %v", err)
		}
		if publishedAgo > 0 {
			if err := db.Exec("UPDATE outbox SET published_at = now() - make_interval(secs => ?) WHERE id = ?",
				publishedAgo.Seconds(), m.ID).Error; err != nil {
				t.Fatalf("age message: %v", err)
			}
		}
		return m
	}

	oldSent := mk(outbox.StatusSent, 48*time.Hour)  // 该删
	freshSent := mk(outbox.StatusSent, time.Minute) // 保留期没到
	pending := mk(outbox.StatusPending, 0)          // 待发，永不删
	failed := mk(outbox.StatusFailed, 48*time.Hour) // 等人看，永不自动删

	r.once(ctx)

	exists := func(id int64) bool {
		return countRows(t, db, "SELECT count(*) FROM outbox WHERE id = $1", id) == 1
	}
	if exists(oldSent.ID) {
		t.Error("old sent message should be purged")
	}
	if !exists(freshSent.ID) {
		t.Error("fresh sent message should be kept")
	}
	if !exists(pending.ID) {
		t.Error("pending message must never be purged")
	}
	if !exists(failed.ID) {
		t.Error("failed message must be kept for humans")
	}
}

// 去重记录跟着过期被清，否则这张表会无限增长。
func TestReaperPurgesConsumedMessagesIntegration(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	r := newReaper(db, 2*time.Minute)

	mid := "11111111-1111-1111-1111-111111111111"
	if err := db.Exec(`INSERT INTO consumed_messages (message_id, topic, consumed_at)
VALUES (?, 'document.import', now() - interval '48 hours')`, mid).Error; err != nil {
		t.Fatalf("insert consumed: %v", err)
	}

	r.once(ctx)

	if n := countRows(t, db, "SELECT count(*) FROM consumed_messages"); n != 0 {
		t.Errorf("consumed rows = %d, want 0 after purge", n)
	}
}
