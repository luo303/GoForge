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

		if _, err := repo.Heartbeat(ctx, claimed.ID, "worker-a", 10*time.Minute); err != nil {
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
		if _, err := repo.Heartbeat(ctx, claimed.ID, "worker-b", testLease); !errors.Is(err, ErrLeaseLost) {
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
		if _, err := repo.Heartbeat(ctx, claimed.ID, "worker-a", testLease); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("renew error = %v, want ErrLeaseLost after requeue", err)
		}
	})
}

func TestFinishIntegration(t *testing.T) {
	db := setupJobDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	t.Run("成功写回终态并释放租约", func(t *testing.T) {
		mustEnqueue(t, repo, 30, "document:30:import")
		claimed, err := repo.Claim(ctx, "worker-a", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := repo.Succeed(ctx, claimed.ID, "worker-a"); err != nil {
			t.Fatalf("succeed: %v", err)
		}
		done, err := repo.Get(ctx, claimed.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if done.Status != StatusSucceeded {
			t.Errorf("status = %s, want succeeded", done.Status)
		}
		if done.FinishedAt == nil {
			t.Error("finished_at should be set")
		}
		if done.LeaseExpiresAt != nil {
			t.Error("lease should be cleared so nothing tries to renew it")
		}
	})

	t.Run("失败保留错误码与原因", func(t *testing.T) {
		mustEnqueue(t, repo, 31, "document:31:import")
		claimed, err := repo.Claim(ctx, "worker-a", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := repo.Fail(ctx, claimed.ID, "worker-a", "parse_error", "文件不是合法 UTF-8"); err != nil {
			t.Fatalf("fail: %v", err)
		}
		done, err := repo.Get(ctx, claimed.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if done.Status != StatusFailed {
			t.Errorf("status = %s, want failed", done.Status)
		}
		if done.LastErrorCode == nil || *done.LastErrorCode != "parse_error" {
			t.Errorf("last_error_code = %v, want parse_error", done.LastErrorCode)
		}
		if done.LastError == nil || *done.LastError == "" {
			t.Error("last_error should keep the human readable reason")
		}
	})

	t.Run("优雅停止把任务放回队列且不消耗尝试次数", func(t *testing.T) {
		mustEnqueue(t, repo, 32, "document:32:import")
		claimed, err := repo.Claim(ctx, "worker-a", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if claimed.Attempts != 1 {
			t.Fatalf("attempts after claim = %d, want 1", claimed.Attempts)
		}
		if err := repo.Release(ctx, claimed.ID, "worker-a"); err != nil {
			t.Fatalf("release: %v", err)
		}
		back, err := repo.Get(ctx, claimed.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if back.Status != StatusQueued {
			t.Errorf("status = %s, want queued", back.Status)
		}
		// 这次是我们自己叫停的，不该算一次失败尝试——否则反复部署会把重试次数耗光。
		if back.Attempts != 0 {
			t.Errorf("attempts = %d, want 0 after release", back.Attempts)
		}
		if back.WorkerID != nil {
			t.Errorf("worker_id = %v, want NULL", back.WorkerID)
		}
	})

	t.Run("退避重排保留尝试次数并推迟可领取时间", func(t *testing.T) {
		mustEnqueue(t, repo, 34, "document:34:import")
		claimed, err := repo.Claim(ctx, "worker-a", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := repo.RetryLater(ctx, claimed.ID, "worker-a", time.Minute, "storage_error", "minio 暂时不可用"); err != nil {
			t.Fatalf("retry later: %v", err)
		}
		back, err := repo.Get(ctx, claimed.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if back.Status != StatusQueued {
			t.Errorf("status = %s, want queued", back.Status)
		}
		// 与优雅停止不同：这次真的尝试过，次数要保留，否则会无限重试。
		if back.Attempts != 1 {
			t.Errorf("attempts = %d, want 1 kept", back.Attempts)
		}
		if back.WorkerID != nil {
			t.Errorf("worker_id = %v, want NULL so anyone can pick it up", back.WorkerID)
		}
		if !back.RunAfter.After(time.Now()) {
			t.Errorf("run_after = %s, want in the future", back.RunAfter)
		}
		if back.LastErrorCode == nil || *back.LastErrorCode != "storage_error" {
			t.Errorf("last_error_code = %v, want storage_error", back.LastErrorCode)
		}
		// 退避期间不该被领走。先清掉前面用例留下的可领取任务，
		// 否则这次 Claim 会拿到别的行，断言就失去意义。
		if err := db.WithContext(ctx).Exec("DELETE FROM jobs WHERE id <> ?", claimed.ID).Error; err != nil {
			t.Fatalf("clean other jobs: %v", err)
		}
		if _, err := repo.Claim(ctx, "worker-b", testLease); !errors.Is(err, ErrNoJob) {
			t.Fatalf("claim during backoff error = %v, want ErrNoJob", err)
		}
	})

	t.Run("失去租约后写终态会失败", func(t *testing.T) {
		mustEnqueue(t, repo, 33, "document:33:import")
		claimed, err := repo.Claim(ctx, "worker-a", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// 接管者拿走了这条任务，旧 worker 不该还能把它标记成成功。
		if err := db.WithContext(ctx).Exec(
			"UPDATE jobs SET worker_id = 'worker-b' WHERE id = ?", claimed.ID).Error; err != nil {
			t.Fatalf("steal job: %v", err)
		}
		if err := repo.Succeed(ctx, claimed.ID, "worker-a"); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("succeed error = %v, want ErrLeaseLost", err)
		}
	})
}

func TestRequeueExpiredLeasesIntegration(t *testing.T) {
	db := setupJobDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	t.Run("租约过期的任务被收回队列且保留尝试次数", func(t *testing.T) {
		mustEnqueue(t, repo, 40, "document:40:import")
		claimed, err := repo.Claim(ctx, "worker-dead", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// 模拟 worker 被 kill -9：状态停在 running，租约早已到期。
		if err := db.WithContext(ctx).Exec(
			"UPDATE jobs SET lease_expires_at = now() - interval '10 minutes' WHERE id = ?",
			claimed.ID).Error; err != nil {
			t.Fatalf("expire lease: %v", err)
		}

		n, err := repo.RequeueExpiredLeases(ctx, testLease)
		if err != nil {
			t.Fatalf("requeue: %v", err)
		}
		if n < 1 {
			t.Fatalf("requeued = %d, want at least 1", n)
		}

		back, err := repo.Get(ctx, claimed.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if back.Status != StatusQueued {
			t.Errorf("status = %s, want queued", back.Status)
		}
		// 与优雅停止不同：这次尝试确实发生过（进程是在执行中死的），要计入 attempts。
		if back.Attempts != 1 {
			t.Errorf("attempts = %d, want 1 kept", back.Attempts)
		}

		// 收回后必须能被重新领走，否则"恢复"只是换了个显示状态。
		again, err := repo.Claim(ctx, "worker-alive", testLease)
		if err != nil {
			t.Fatalf("claim after requeue: %v", err)
		}
		if again.ID != claimed.ID {
			t.Fatalf("claimed job %d, want %d", again.ID, claimed.ID)
		}
	})

	t.Run("租约未过期的任务不会被抢走", func(t *testing.T) {
		mustEnqueue(t, repo, 41, "document:41:import")
		claimed, err := repo.Claim(ctx, "worker-alive", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		n, err := repo.RequeueExpiredLeases(ctx, testLease)
		if err != nil {
			t.Fatalf("requeue: %v", err)
		}
		if n != 0 {
			t.Fatalf("requeued = %d, want 0 for a live lease", n)
		}
		still, err := repo.Get(ctx, claimed.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if still.Status != StatusRunning {
			t.Errorf("status = %s, want running", still.Status)
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

// TestCancelIntegration 覆盖取消的两条路径：
// 排队中的任务可以直接取消，运行中的只能置 cancelling 等 worker 确认。
func TestCancelIntegration(t *testing.T) {
	db := setupJobDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	t.Run("排队中的任务取消后立刻是 cancelled", func(t *testing.T) {
		j := mustEnqueue(t, repo, 50, "document:50:import")

		id, kind, status, err := repo.RequestCancel(ctx, 50)
		if err != nil {
			t.Fatalf("request cancel: %v", err)
		}
		if id != j.ID || kind != "document.import" {
			t.Errorf("cancelled job = %d/%s, want %d/document.import", id, kind, j.ID)
		}
		if status != StatusCancelled {
			t.Errorf("status = %s, want cancelled for a queued job", status)
		}
		// 不能只信返回值，回查一次确认落库。
		cancelled, err := repo.Get(ctx, j.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if cancelled.Status != StatusCancelled || cancelled.FinishedAt == nil {
			t.Errorf("stored job = %s finished_at=%v, want cancelled with finished_at", cancelled.Status, cancelled.FinishedAt)
		}

		// 没有进行中的任务了：再取消一次应该明确报"没有"。
		if _, _, _, err := repo.RequestCancel(ctx, 50); !errors.Is(err, ErrNoJob) {
			t.Fatalf("second cancel error = %v, want ErrNoJob", err)
		}
	})

	t.Run("运行中的任务转 cancelling 由 worker 确认", func(t *testing.T) {
		j := mustEnqueue(t, repo, 51, "document:51:import")
		claimed, err := repo.Claim(ctx, "worker-a", testLease)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if claimed.ID != j.ID {
			t.Fatalf("claimed %d, want %d", claimed.ID, j.ID)
		}

		_, _, status, err := repo.RequestCancel(ctx, 51)
		if err != nil {
			t.Fatalf("request cancel: %v", err)
		}
		if status != StatusCancelling {
			t.Errorf("status = %s, want cancelling for a running job", status)
		}

		// worker 心跳必须能看到 cancelling，才能停手。
		beatStatus, err := repo.Heartbeat(ctx, claimed.ID, "worker-a", testLease)
		if err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		if beatStatus != StatusCancelling {
			t.Errorf("heartbeat status = %s, want cancelling", beatStatus)
		}

		if err := repo.Cancel(ctx, claimed.ID, "worker-a"); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		final, err := repo.Get(ctx, claimed.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if final.Status != StatusCancelled {
			t.Errorf("status = %s, want cancelled", final.Status)
		}
	})
}
