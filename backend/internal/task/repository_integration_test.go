package task

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
	"github.com/luo303/mindos/backend/migrations"
)

// 本文件覆盖"至少一次投递"下的执行闸门。这是消费幂等的第二道防线：
// 第一道是 consumed_messages 的去重表，它可能在"处理完、还没写记录"的窗口里
// 失效；那时唯一能挡住重复执行的就是 MarkRunning 的条件更新。
//
// 运行方式（先启动 compose）：
//
//	MINDOS_TEST_DSN="host=localhost port=5432 user=mindos password=mindos_dev_password dbname=postgres sslmode=disable" \
//	go test -count=1 ./internal/task/
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

	testDB := fmt.Sprintf("mindos_task_test_%d", time.Now().UnixNano())
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

func newTask(t *testing.T, repo *Repository, docID int64) *Task {
	t.Helper()
	tt := New("document.import", docID)
	if err := AppendTx(context.Background(), repo.db.WithContext(context.Background()), tt); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	return tt
}

// 重复投递时只有第一个能把 pending 推到 running——这就是"至少一次"下的执行闸门。
func TestMarkRunningIsIdempotentGateIntegration(t *testing.T) {
	repo := NewRepository(setupDB(t))
	ctx := context.Background()
	tt := newTask(t, repo, 100)

	first, err := repo.MarkRunning(ctx, tt.ID)
	if err != nil {
		t.Fatalf("first mark running: %v", err)
	}
	if !first {
		t.Fatal("first attempt must win the gate")
	}

	// 第二条重复消息到达：状态已经不是 pending，抢不到。
	second, err := repo.MarkRunning(ctx, tt.ID)
	if err != nil {
		t.Fatalf("second mark running: %v", err)
	}
	if second {
		t.Fatal("second attempt must not win the gate (would run the handler twice)")
	}

	got, err := repo.Get(ctx, tt.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (the rejected attempt must not count)", got.Attempts)
	}
}

// 已经跑完的任务再收到同一条消息，同样不该再执行。
func TestMarkRunningRefusesFinishedTaskIntegration(t *testing.T) {
	repo := NewRepository(setupDB(t))
	ctx := context.Background()
	tt := newTask(t, repo, 101)

	if _, err := repo.MarkRunning(ctx, tt.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := repo.MarkSucceeded(ctx, tt.ID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}

	again, err := repo.MarkRunning(ctx, tt.ID)
	if err != nil {
		t.Fatalf("mark running again: %v", err)
	}
	if again {
		t.Fatal("a succeeded task must not be revived by a duplicate delivery")
	}
}

// 失败重试走的是"先放回 pending，再由新消息推进"。这条路径必须真的能再推进，
// 否则第一轮失败之后任务就再也跑不起来了。
func TestMarkPendingAllowsAnotherAttemptIntegration(t *testing.T) {
	repo := NewRepository(setupDB(t))
	ctx := context.Background()
	tt := newTask(t, repo, 102)

	if _, err := repo.MarkRunning(ctx, tt.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := repo.MarkPending(ctx, tt.ID); err != nil {
		t.Fatalf("mark pending: %v", err)
	}

	again, err := repo.MarkRunning(ctx, tt.ID)
	if err != nil {
		t.Fatalf("mark running after reset: %v", err)
	}
	if !again {
		t.Fatal("a task reset to pending must be able to run again")
	}

	got, err := repo.Get(ctx, tt.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", got.Attempts)
	}
}

// Active 只认未完成的任务：取消要靠它判断"排着队还是在跑"。
func TestActiveIgnoresFinishedTasksIntegration(t *testing.T) {
	repo := NewRepository(setupDB(t))
	ctx := context.Background()
	tt := newTask(t, repo, 103)

	active, err := repo.Active(ctx, TargetDocument, 103)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if active.ID != tt.ID {
		t.Errorf("active = %d, want %d", active.ID, tt.ID)
	}

	if err := repo.MarkSucceeded(ctx, tt.ID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}
	if _, err := repo.Active(ctx, TargetDocument, 103); err != ErrNotFound {
		t.Errorf("error = %v, want ErrNotFound after the task finished", err)
	}
}

// 条件取消：只有还没被领走才抢得到。抢不到时调用方要改走"标记取消中"。
func TestCancelPendingOnlyWorksWhileQueuedIntegration(t *testing.T) {
	repo := NewRepository(setupDB(t))
	ctx := context.Background()

	queued := newTask(t, repo, 104)
	ok, err := repo.CancelPending(ctx, queued.ID)
	if err != nil {
		t.Fatalf("cancel pending: %v", err)
	}
	if !ok {
		t.Fatal("a queued task should be cancellable")
	}
	if got, _ := repo.Get(ctx, queued.ID); got.Status != StatusCancelled {
		t.Errorf("status = %s, want cancelled", got.Status)
	}

	// 已被领走（running）：抢不到，调用方据此改走 cancelling。
	running := newTask(t, repo, 105)
	if _, err := repo.MarkRunning(ctx, running.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	ok, err = repo.CancelPending(ctx, running.ID)
	if err != nil {
		t.Fatalf("cancel running: %v", err)
	}
	if ok {
		t.Fatal("a running task must not be cancelled directly")
	}
	if got, _ := repo.Get(ctx, running.ID); got.Status != StatusRunning {
		t.Errorf("status = %s, want running (untouched)", got.Status)
	}
}
