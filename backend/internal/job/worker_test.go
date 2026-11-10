package job

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// 本文件测 worker 的循环、心跳与停止。用内存替身而不是真库：
// 要验证的是"任务失败后写了什么状态""停止时有没有放回队列"这类逻辑，
// 与 SQL 无关；真库那一层由 repository_integration_test.go 覆盖。

type fakeStore struct {
	mu        sync.Mutex
	pending   []*Job
	claimed   []int64
	succeeded []int64
	released  []int64
	retried   []int64
	retryWait []time.Duration
	failed    map[int64]string // job_id → code
	failMsg   map[int64]string
	renewErr  error
	// heartbeatStatus 是心跳回报的状态；零值视为 running。
	heartbeatStatus Status
	cancelled       []int64
	claimErrs       []error // 依次返回的错误，用完后正常领取
	recovered       int     // RequeueExpiredLeases 被调用的次数
	requeued        int64   // 每次返回的回收条数
}

func newFakeStore(jobs ...*Job) *fakeStore {
	return &fakeStore{pending: jobs, failed: map[int64]string{}, failMsg: map[int64]string{}}
}

func (f *fakeStore) Claim(ctx context.Context, workerID string, lease time.Duration) (*Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.claimErrs) > 0 {
		err := f.claimErrs[0]
		f.claimErrs = f.claimErrs[1:]
		return nil, err
	}
	if len(f.pending) == 0 {
		return nil, ErrNoJob
	}
	j := f.pending[0]
	f.pending = f.pending[1:]
	j.Status = StatusRunning
	j.Attempts++
	worker := workerID
	j.WorkerID = &worker
	f.claimed = append(f.claimed, j.ID)
	return j, nil
}

func (f *fakeStore) Heartbeat(ctx context.Context, id int64, workerID string, lease time.Duration) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renewErr != nil {
		return "", f.renewErr
	}
	if f.heartbeatStatus == "" {
		return StatusRunning, nil
	}
	return f.heartbeatStatus, nil
}

func (f *fakeStore) Cancel(ctx context.Context, id int64, workerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	return nil
}

func (f *fakeStore) Succeed(ctx context.Context, id int64, workerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.succeeded = append(f.succeeded, id)
	return nil
}

func (f *fakeStore) Fail(ctx context.Context, id int64, workerID, code, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed[id] = code
	f.failMsg[id] = message
	return nil
}

func (f *fakeStore) RetryLater(ctx context.Context, id int64, workerID string, delay time.Duration, code, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retried = append(f.retried, id)
	f.retryWait = append(f.retryWait, delay)
	return nil
}

func (f *fakeStore) Release(ctx context.Context, id int64, workerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, id)
	return nil
}

func (f *fakeStore) RequeueExpiredLeases(ctx context.Context, grace time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recovered++
	return f.requeued, nil
}

func (f *fakeStore) counts() (claimed, succeeded, released int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.claimed), len(f.succeeded), len(f.released)
}

// runWorker 起一个 worker，返回"停止函数"与"已退出"通道。
func runWorker(t *testing.T, st store, opts Options, register func(*Worker)) (stop func(), done chan struct{}) {
	t.Helper()
	w := NewWorker(st, zap.NewNop(), opts)
	if register != nil {
		register(w)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	return cancel, done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestWorkerSucceeds(t *testing.T) {
	st := newFakeStore(&Job{ID: 1, Kind: "document.import"})
	var handled sync.WaitGroup
	handled.Add(1)

	stop, done := runWorker(t, st, Options{WorkerID: "w1", Lease: 3 * time.Second, PollInterval: 10 * time.Millisecond},
		func(w *Worker) {
			w.Register("document.import", func(ctx context.Context, j *Job) error {
				defer handled.Done()
				return nil
			})
		})
	defer func() { stop(); <-done }()

	waitFor(t, "job succeeded", func() bool {
		_, ok, _ := st.counts()
		return ok == 1
	})
}

func TestWorkerFailsOnHandlerError(t *testing.T) {
	// MaxAttempts=1：模拟"这次就是最后一次机会"，此时不应再排队重试。
	st := newFakeStore(&Job{ID: 2, Kind: "document.import", MaxAttempts: 1})

	stop, done := runWorker(t, st, Options{WorkerID: "w1", Lease: 3 * time.Second, PollInterval: 10 * time.Millisecond},
		func(w *Worker) {
			w.Register("document.import", func(ctx context.Context, j *Job) error {
				return errors.New("解析失败：不支持的文件类型")
			})
		})
	defer func() { stop(); <-done }()

	waitFor(t, "job failed", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.failed[2] != ""
	})
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.failed[2] != "handler_error" {
		t.Errorf("error code = %s, want handler_error", st.failed[2])
	}
	if st.failMsg[2] == "" {
		t.Error("failure message should be recorded for humans")
	}
}

// 可恢复错误且还有机会：应该退避重排，而不是判死。
func TestWorkerRetriesRecoverableError(t *testing.T) {
	st := newFakeStore(&Job{ID: 20, Kind: "document.import", MaxAttempts: 3})

	stop, done := runWorker(t, st, Options{WorkerID: "w1", Lease: 3 * time.Second, PollInterval: 10 * time.Millisecond},
		func(w *Worker) {
			w.Register("document.import", func(ctx context.Context, j *Job) error {
				return errors.New("minio: connection reset")
			})
		})
	defer func() { stop(); <-done }()

	waitFor(t, "job retried", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.retried) == 1
	})

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.failed) != 0 {
		t.Errorf("failed = %v, want none while retries remain", st.failed)
	}
	// 等待时长必须为正：0 等于立即重试，会把对方继续打爆。
	if st.retryWait[0] <= 0 {
		t.Errorf("retry delay = %s, want positive backoff", st.retryWait[0])
	}
}

// 被标记为永久失败的错误：即使还有重试次数也不该再排队。
func TestWorkerFailsOnPermanentError(t *testing.T) {
	st := newFakeStore(&Job{ID: 21, Kind: "document.import", MaxAttempts: 3})

	stop, done := runWorker(t, st, Options{WorkerID: "w1", Lease: 3 * time.Second, PollInterval: 10 * time.Millisecond},
		func(w *Worker) {
			w.Register("document.import", func(ctx context.Context, j *Job) error {
				return Permanent(errors.New("unsupported file type"))
			})
		})
	defer func() { stop(); <-done }()

	waitFor(t, "job failed", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.failed[21] != ""
	})
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.retried) != 0 {
		t.Errorf("retried = %v, want none for a permanent error", st.retried)
	}
}

func TestWorkerRejectsUnregisteredKind(t *testing.T) {
	st := newFakeStore(&Job{ID: 3, Kind: "document.unknown"})

	stop, done := runWorker(t, st, Options{WorkerID: "w1", Lease: 3 * time.Second, PollInterval: 10 * time.Millisecond}, nil)
	defer func() { stop(); <-done }()

	waitFor(t, "job failed", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.failed[3] != ""
	})
	st.mu.Lock()
	defer st.mu.Unlock()
	// 部署问题不该被无限重试，错误码要能被程序识别。
	if st.failed[3] != "unsupported_kind" {
		t.Errorf("error code = %s, want unsupported_kind", st.failed[3])
	}
}

func TestWorkerReleasesJobOnShutdown(t *testing.T) {
	st := newFakeStore(&Job{ID: 4, Kind: "document.import"})
	started := make(chan struct{})

	stop, done := runWorker(t, st, Options{WorkerID: "w1", Lease: 3 * time.Second, PollInterval: 10 * time.Millisecond},
		func(w *Worker) {
			w.Register("document.import", func(ctx context.Context, j *Job) error {
				close(started)
				// 模拟一个跑得比较久的解析：只有 ctx 取消才会返回。
				<-ctx.Done()
				return ctx.Err()
			})
		})

	<-started
	stop() // 进程要停了
	<-done // Run 必须等这条任务收尾后才返回

	st.mu.Lock()
	defer st.mu.Unlock()
	// 放回队列，而不是记成失败：这次是我们自己叫停的，
	// 记失败会让反复部署把 max_attempts 耗光。
	if len(st.released) != 1 || st.released[0] != 4 {
		t.Fatalf("released = %v, want [4]", st.released)
	}
	if len(st.failed) != 0 {
		t.Errorf("failed = %v, want none", st.failed)
	}
}

func TestWorkerStopsOnLeaseLostWithoutWritingState(t *testing.T) {
	st := newFakeStore(&Job{ID: 5, Kind: "document.import"})
	st.renewErr = ErrLeaseLost
	aborted := make(chan struct{})

	stop, done := runWorker(t, st, Options{WorkerID: "w1", Lease: 3 * time.Second, PollInterval: 10 * time.Millisecond},
		func(w *Worker) {
			w.Register("document.import", func(ctx context.Context, j *Job) error {
				<-ctx.Done() // 等心跳发现租约丢失
				close(aborted)
				return ctx.Err()
			})
		})
	defer func() { stop(); <-done }()

	select {
	case <-aborted:
	case <-time.After(3 * time.Second):
		t.Fatal("handler was not cancelled after lease lost")
	}
	waitFor(t, "worker idle after lease lost", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.pending) == 0 && len(st.claimed) == 1
	})

	st.mu.Lock()
	defer st.mu.Unlock()
	// 租约丢了就什么都不写：状态归接管者，我们写什么都可能覆盖别人的结果。
	if len(st.succeeded) != 0 || len(st.failed) != 0 || len(st.released) != 0 {
		t.Fatalf("expected no state write, got succeeded=%v failed=%v released=%v",
			st.succeeded, st.failed, st.released)
	}
}

func TestWorkerRecoversExpiredLeasesOnStart(t *testing.T) {
	st := newFakeStore()
	st.requeued = 2

	stop, done := runWorker(t, st, Options{WorkerID: "w1", Lease: 3 * time.Second, PollInterval: 10 * time.Millisecond}, nil)
	defer func() { stop(); <-done }()

	// 启动就要扫一次：上一任就是在启动前崩的，等一个完整周期太慢。
	waitFor(t, "recovery pass on start", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.recovered >= 1
	})
}

// 心跳回报 cancelling 时：handler 被中断，任务写 cancelled，
// 而不是被当成失败去重试。
func TestWorkerCancelsJobOnRequest(t *testing.T) {
	st := newFakeStore(&Job{ID: 22, Kind: "document.import", MaxAttempts: 3})
	st.heartbeatStatus = StatusCancelling
	aborted := make(chan struct{})

	stop, done := runWorker(t, st, Options{WorkerID: "w1", Lease: 3 * time.Second, PollInterval: 10 * time.Millisecond},
		func(w *Worker) {
			w.Register("document.import", func(ctx context.Context, j *Job) error {
				<-ctx.Done()
				close(aborted)
				return ctx.Err()
			})
		})
	defer func() { stop(); <-done }()

	select {
	case <-aborted:
	case <-time.After(3 * time.Second):
		t.Fatal("handler was not interrupted after cancel request")
	}
	waitFor(t, "job cancelled", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.cancelled) == 1
	})

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.retried) != 0 || len(st.failed) != 0 {
		t.Errorf("cancelled job should not be retried or failed: retried=%v failed=%v",
			st.retried, st.failed)
	}
	if len(st.succeeded) != 0 {
		t.Error("cancelled job must not be marked succeeded")
	}
}

// 取消之后必须回调业务方：否则文档会永久停在"取消中"，
// 因为 Worker 只写任务状态，不认识文档。
func TestWorkerNotifiesOnCancelled(t *testing.T) {
	st := newFakeStore(&Job{ID: 23, Kind: "document.import", MaxAttempts: 3})
	st.heartbeatStatus = StatusCancelling
	notified := make(chan int64, 1)

	stop, done := runWorker(t, st, Options{
		WorkerID:     "w1",
		Lease:        3 * time.Second,
		PollInterval: 10 * time.Millisecond,
		OnCancelled: func(ctx context.Context, j *Job) {
			notified <- j.ID
		},
	}, func(w *Worker) {
		w.Register("document.import", func(ctx context.Context, j *Job) error {
			<-ctx.Done()
			return ctx.Err()
		})
	})
	defer func() { stop(); <-done }()

	select {
	case id := <-notified:
		if id != 23 {
			t.Errorf("notified job = %d, want 23", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnCancelled was not called")
	}
}

func TestWorkerKeepsRunningWhenClaimFails(t *testing.T) {
	st := newFakeStore(&Job{ID: 6, Kind: "document.import"})
	st.claimErrs = []error{errors.New("connection reset"), errors.New("connection reset")}

	stop, done := runWorker(t, st, Options{WorkerID: "w1", Lease: 3 * time.Second, PollInterval: 10 * time.Millisecond},
		func(w *Worker) {
			w.Register("document.import", func(ctx context.Context, j *Job) error { return nil })
		})
	defer func() { stop(); <-done }()

	// 两次领取失败之后应该照常领到任务，而不是把整个 worker 退掉。
	waitFor(t, "job succeeded after transient claim errors", func() bool {
		_, ok, _ := st.counts()
		return ok == 1
	})
}
