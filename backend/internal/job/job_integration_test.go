package job

import (
	"context"
	"database/sql"
	"errors"
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

// 本文件是真实 PostgreSQL 集成测试：迁移约束与去重索引必须在库里验证，
// 用 SQLite 或内存替身测不出部分唯一索引的行为。
//
// 运行方式（先起 compose）：
//
//	MINDOS_TEST_DSN="host=localhost port=5432 user=mindos password=mindos_dev_password dbname=postgres sslmode=disable" \
//	go test -race -count=1 -run Integration ./internal/job/
//
// 未设置变量时全部跳过；每个用例建独立临时库并在结束时 DROP，不碰开发库。
const testDSNEnv = "MINDOS_TEST_DSN"

func setupJobDB(t *testing.T) *gorm.DB {
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

	testDB := fmt.Sprintf("mindos_job_test_%d", time.Now().UnixNano())
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

func newJob(kind Kind, key string, payload Payload) *Job {
	return &Job{
		Kind:        kind,
		Status:      StatusQueued,
		DedupeKey:   &key,
		Payload:     payload,
		MaxAttempts: 3,
		RunAfter:    time.Now().UTC(),
	}
}

// TestJobsSchemaIntegration 验证两条"数据库兜底"的规则真的生效：
// 非法状态写不进去，同一件工作不会同时存在两个未完成任务。
func TestJobsSchemaIntegration(t *testing.T) {
	db := setupJobDB(t)
	ctx := context.Background()

	t.Run("非法状态被 CHECK 拒绝", func(t *testing.T) {
		bad := newJob("document.import", "document:1:import", Payload{"document_id": 1})
		bad.Status = "done" // 状态机里没有这个值
		if err := db.WithContext(ctx).Create(bad).Error; err == nil {
			t.Fatal("insert job with invalid status should fail")
		}
	})

	t.Run("同一 dedupe_key 只能有一个未完成任务", func(t *testing.T) {
		key := "document:2:import"
		if err := db.WithContext(ctx).Create(newJob("document.import", key, Payload{"document_id": 2})).Error; err != nil {
			t.Fatalf("create first job: %v", err)
		}
		err := db.WithContext(ctx).Create(newJob("document.import", key, Payload{"document_id": 2})).Error
		if !errors.Is(err, gorm.ErrDuplicatedKey) {
			t.Fatalf("second insert error = %v, want duplicated key", err)
		}
	})

	t.Run("已完成的任务不占用去重名额", func(t *testing.T) {
		key := "document:3:import"
		first := newJob("document.import", key, Payload{"document_id": 3})
		first.Status = StatusSucceeded
		if err := db.WithContext(ctx).Create(first).Error; err != nil {
			t.Fatalf("create succeeded job: %v", err)
		}
		// 同一件事重新提交是允许的：部分唯一索引只约束 queued / running。
		if err := db.WithContext(ctx).Create(newJob("document.import", key, Payload{"document_id": 3})).Error; err != nil {
			t.Fatalf("requeue after success should be allowed: %v", err)
		}
	})

	t.Run("dedupe_key 为空时不做去重", func(t *testing.T) {
		// NULL 之间互不相等，所以两条都会被接受。
		for i := 0; i < 2; i++ {
			j := newJob("document.reindex", "", Payload{"document_id": 4})
			j.DedupeKey = nil
			if err := db.WithContext(ctx).Create(j).Error; err != nil {
				t.Fatalf("create job without dedupe key #%d: %v", i, err)
			}
		}
	})

	t.Run("payload 往返保持 JSON 结构", func(t *testing.T) {
		created := newJob("document.import", "document:5:import", Payload{"document_id": 5, "note": "带中文"})
		if err := db.WithContext(ctx).Create(created).Error; err != nil {
			t.Fatalf("create job: %v", err)
		}
		var loaded Job
		if err := db.WithContext(ctx).First(&loaded, created.ID).Error; err != nil {
			t.Fatalf("load job: %v", err)
		}
		id, err := loaded.DocumentID()
		if err != nil {
			t.Fatalf("document id: %v", err)
		}
		if id != 5 {
			t.Errorf("document id = %d, want 5", id)
		}
		if loaded.Payload["note"] != "带中文" {
			t.Errorf("payload note = %v", loaded.Payload["note"])
		}
	})
}
