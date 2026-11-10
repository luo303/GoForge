package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖 Phase 6 的领取与租约语义。这些行为只能在真实库上验证：
// FOR UPDATE SKIP LOCKED、部分唯一索引、now() 的时间判断都是数据库行为，
// 用替身测等于测自己写的假实现。

const testLease = 30 * time.Second

func mustEnqueue(t *testing.T, repo *Repository, docID int64, key string) *Job {
	t.Helper()
	j, created, err := repo.Enqueue(context.Background(), EnqueueInput{
		Kind:      "document.import",
		Payload:   Payload{"document_id": docID},
		DedupeKey: &key,
	})
	if err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if !created {
		t.Fatalf("enqueue returned existing job %d, want a new one", j.ID)
	}
	return j
}

func TestClaimIntegration(t *testing.T) {
	db := setupJobDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	t.Run("领取后状态与租约就位", func(t *testing.T) {
		j := mustEnqueue(t, repo, 1, "document:1:import")

		claimed, err := repo.Claim(ctx, "worker-a", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if claimed.ID != j.ID {
			t.Fatalf("claimed job %d, want %d", claimed.ID, j.ID)
		}
		if claimed.Status != StatusRunning {
			t.Errorf("status = %s, want running", claimed.Status)
		}
		// attempts 在领取时加：进程被 kill 也算一次尝试，否则无限崩溃的任务永远重试不完。
		if claimed.Attempts != 1 {
			t.Errorf("attempts = %d, want 1", claimed.Attempts)
		}
		if claimed.WorkerID == nil || *claimed.WorkerID != "worker-a" {
			t.Errorf("worker_id = %v, want worker-a", claimed.WorkerID)
		}
		if claimed.LeaseExpiresAt == nil || !claimed.LeaseExpiresAt.After(time.Now()) {
			t.Errorf("lease_expires_at = %v, want in the future", claimed.LeaseExpiresAt)
		}
		if claimed.StartedAt == nil {
			t.Error("started_at should be set on claim")
		}
	})

	t.Run("没活可干时返回 ErrNoJob", func(t *testing.T) {
		if _, err := repo.Claim(ctx, "worker-a", testLease); !errors.Is(err, ErrNoJob) {
			t.Fatalf("claim error = %v, want ErrNoJob", err)
		}
	})

	t.Run("run_after 未到不领取", func(t *testing.T) {
		key := "document:2:import"
		if _, _, err := repo.Enqueue(ctx, EnqueueInput{
			Kind:      "document.import",
			Payload:   Payload{"document_id": 2},
			DedupeKey: &key,
			RunAfter:  time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if _, err := repo.Claim(ctx, "worker-a", testLease); !errors.Is(err, ErrNoJob) {
			t.Fatalf("claim error = %v, want ErrNoJob for future run_after", err)
		}
	})

	t.Run("并发领取时每条任务只被领走一次", func(t *testing.T) {
		// 清空上一组用例留下的任务，避免干扰计数。
		if err := db.WithContext(ctx).Exec("DELETE FROM jobs").Error; err != nil {
			t.Fatalf("clean jobs: %v", err)
		}
		const total = 20
		for i := 0; i < total; i++ {
			mustEnqueue(t, repo, int64(i), fmt.Sprintf("document:%d:import", i))
		}

		var (
			mu     sync.Mutex
			seen   = map[int64]int{}
			claims int
			wg     sync.WaitGroup
		)
		// 8 个 worker 抢 20 条任务：同一 id 出现两次就说明领取不是原子的。
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(worker string) {
				defer wg.Done()
				for {
					claimed, err := repo.Claim(ctx, worker, testLease)
					if errors.Is(err, ErrNoJob) {
						return
					}
					if err != nil {
						t.Errorf("claim: %v", err)
						return
					}
					mu.Lock()
					seen[claimed.ID]++
					claims++
					mu.Unlock()
				}
			}(fmt.Sprintf("worker-%d", w))
		}
		wg.Wait()

		if claims != total {
			t.Fatalf("claims = %d, want %d", claims, total)
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("job %d claimed %d times, want 1", id, n)
			}
		}
	})
}

func TestLeaseIntegration(t *testing.T) {
	db := setupJobDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	t.Run("续约把到期时间往后推", func(t *testing.T) {
		mustEnqueue(t, repo, 10, "document:10:import")
		claimed, err := repo.Claim(ctx, "worker-a", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		before := *claimed.LeaseExpiresAt

		if err := repo.RenewLease(ctx, claimed.ID, "worker-a", 10*time.Minute); err != nil {
			t.Fatalf("renew: %v", err)
		}
		after, err := repo.Get(ctx, claimed.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if after.LeaseExpiresAt == nil || !after.LeaseExpiresAt.After(before) {
			t.Errorf("lease not extended: before=%v after=%v", before, after.LeaseExpiresAt)
		}
	})

	t.Run("别的 worker 续约会失败", func(t *testing.T) {
		mustEnqueue(t, repo, 11, "document:11:import")
		claimed, err := repo.Claim(ctx, "worker-a", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// 这是"我已被接管"的信号：必须让旧 worker 停下来，
		// 否则两个进程会同时写同一个文档的分块。
		if err := repo.RenewLease(ctx, claimed.ID, "worker-b", testLease); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("renew error = %v, want ErrLeaseLost", err)
		}
	})

	t.Run("任务被放回队列后续约失败", func(t *testing.T) {
		mustEnqueue(t, repo, 12, "document:12:import")
		claimed, err := repo.Claim(ctx, "worker-a", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// 模拟补扫：租约过期后被置回 queued。
		if err := db.WithContext(ctx).Exec(
			"UPDATE jobs SET status = 'queued', worker_id = NULL WHERE id = ?", claimed.ID).Error; err != nil {
			t.Fatalf("requeue: %v", err)
		}
		if err := repo.RenewLease(ctx, claimed.ID, "worker-a", testLease); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("renew error = %v, want ErrLeaseLost after requeue", err)
		}
	})
}

func TestEnqueueDedupeIntegration(t *testing.T) {
	db := setupJobDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	t.Run("同一件事重复提交复用未完成任务", func(t *testing.T) {
		first := mustEnqueue(t, repo, 20, "document:20:import")
		key := "document:20:import"
		second, created, err := repo.Enqueue(ctx, EnqueueInput{
			Kind:      "document.import",
			Payload:   Payload{"document_id": 20},
			DedupeKey: &key,
		})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if created {
			t.Fatal("second enqueue should reuse the active job, not create a new one")
		}
		if second.ID != first.ID {
			t.Fatalf("returned job %d, want %d", second.ID, first.ID)
		}
	})

	t.Run("上一个完成后可以再次提交", func(t *testing.T) {
		first := mustEnqueue(t, repo, 21, "document:21:import")
		if err := db.WithContext(ctx).Exec(
			"UPDATE jobs SET status = 'succeeded' WHERE id = ?", first.ID).Error; err != nil {
			t.Fatalf("finish job: %v", err)
		}
		key := "document:21:import"
		second, created, err := repo.Enqueue(ctx, EnqueueInput{
			Kind:      "document.import",
			Payload:   Payload{"document_id": 21},
			DedupeKey: &key,
		})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if !created || second.ID == first.ID {
			t.Fatalf("expected a new job after previous one finished, got created=%v id=%d", created, second.ID)
		}
	})
}
