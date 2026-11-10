package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// HandlerFunc 执行一条任务。返回 nil 表示成功；返回错误表示这次尝试失败。
//
// 三条约定，写在这里因为每个 handler 都要遵守：
//  1. **必须响应 ctx**：取消与优雅停止都靠它传播，卡住不动会让进程关不掉。
//  2. **必须可重跑**：同一个任务可能被执行两次（租约过期被接管、优雅停止后重放），
//     所以"先删后插一个新事务"这类写法是必须的，不能靠"上次做到哪了"。
//  3. **不要自己改任务状态**：状态由 worker 统一写回，handler 只管干活与报错。
type HandlerFunc func(ctx context.Context, j *Job) error

// store 是本文件对仓储的最小需求。
//
// 定义成接口而不是直接依赖 *Repository，是为了让 worker 的循环、心跳、停止
// 这些逻辑可以用内存替身测试——它们的正确性与 SQL 无关，却最容易写错。
type store interface {
	Claim(ctx context.Context, workerID string, lease time.Duration) (*Job, error)
	RenewLease(ctx context.Context, id int64, workerID string, lease time.Duration) error
	Succeed(ctx context.Context, id int64, workerID string) error
	Fail(ctx context.Context, id int64, workerID, code, message string) error
	Release(ctx context.Context, id int64, workerID string) error
}

// 默认值。生产装配时会被配置覆盖，这里只是让零值 Options 也能安全运行。
const (
	defaultConcurrency  = 1
	defaultLease        = 30 * time.Second
	defaultPollInterval = 500 * time.Millisecond
	// 租约不能太短：心跳间隔是 lease/3，太短会让"网络抖一下"就丢租约。
	minLease = 3 * time.Second
)

// Options 是 worker 的装配参数。
type Options struct {
	// WorkerID 标识"谁在跑"，会写进 jobs.worker_id，也是续约的凭据。
	// 同一条任务被两个进程抢时，靠它区分谁的续约有效。
	WorkerID string
	// Concurrency 是同一进程内并发执行的任务数。
	// 单个 worker 进程内并发，而不是"一个任务一个 goroutine"：后者并发无上限，
	// 数据库连接池和模型额度都会被瞬间打满。
	Concurrency int
	// Lease 是租约长度。worker 每 lease/3 续一次；进程死掉后 lease 到期，
	// 任务才能被别人接管——所以它同时也是"崩溃后多久能自愈"的上限。
	Lease time.Duration
	// PollInterval 是没活干时的轮询间隔。
	// Phase 6 用它换简单：轮询不需要额外组件，代价是空闲时有极轻的数据库压力。
	// Phase 16 会改成 MQ 唤醒，但那时也只是"少轮询几次"，任务语义不变。
	PollInterval time.Duration
}

// Worker 是进程内的任务执行者。
//
// 它不持有队列：队列就是 jobs 表本身。所以进程重启、多开一个实例，
// 看到的都是同一份任务，不需要任何"队列同步"。
type Worker struct {
	store     store
	log       *zap.Logger
	id        string
	lease     time.Duration
	poll      time.Duration
	workers   int
	handlers  map[Kind]HandlerFunc
	handlersM sync.RWMutex
}

// NewWorker 创建 worker。handlers 通过 Register 在启动前注册完毕。
func NewWorker(st store, log *zap.Logger, opts Options) *Worker {
	if opts.Concurrency <= 0 {
		opts.Concurrency = defaultConcurrency
	}
	if opts.Lease <= 0 {
		opts.Lease = defaultLease
	}
	if opts.Lease < minLease {
		opts.Lease = minLease
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.WorkerID == "" {
		opts.WorkerID = "worker"
	}
	return &Worker{
		store:    st,
		log:      log,
		id:       opts.WorkerID,
		lease:    opts.Lease,
		poll:     opts.PollInterval,
		workers:  opts.Concurrency,
		handlers: make(map[Kind]HandlerFunc),
	}
}

// Register 注册某种任务的处理函数。
//
// 注册在启动前完成，运行期不再修改——省掉并发改 map 的风险。
// 重复注册同一种 kind 视为装配错误，直接 panic：这种错误只会在写代码时出现，
// 静默覆盖会让"为什么我的 handler 没跑"变成一场排查。
func (w *Worker) Register(kind Kind, fn HandlerFunc) {
	w.handlersM.Lock()
	defer w.handlersM.Unlock()
	if _, exists := w.handlers[kind]; exists {
		panic(fmt.Sprintf("job: handler for kind %q registered twice", kind))
	}
	w.handlers[kind] = fn
}

func (w *Worker) handlerFor(kind Kind) (HandlerFunc, bool) {
	w.handlersM.RLock()
	defer w.handlersM.RUnlock()
	fn, ok := w.handlers[kind]
	return fn, ok
}

// Run 启动 workers 个领取循环，阻塞到 ctx 被取消且所有在跑的任务都已收尾。
//
// 返回值只有一个 nil：worker 的日常错误（领不到任务、某条任务失败）都记日志
// 并继续跑，不该让整个服务退出。真正需要退出的是 ctx 取消，那属于正常关闭。
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("job worker started",
		zap.String("worker_id", w.id),
		zap.Int("concurrency", w.workers),
		zap.Duration("lease", w.lease),
		zap.Duration("poll_interval", w.poll),
	)

	var wg sync.WaitGroup
	for i := 0; i < w.workers; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			w.loop(ctx, slot)
		}(i)
	}

	<-ctx.Done()
	// 等所有 loop 退出：它们手里可能还有正在跑的任务，
	// 必须等这些任务把状态写好，进程才能真的结束。
	wg.Wait()
	w.log.Info("job worker stopped", zap.String("worker_id", w.id))
	return nil
}

// loop 是一个领取循环。
func (w *Worker) loop(ctx context.Context, slot int) {
	for {
		// 先看是否该停：ctx 取消后不再领新任务，
		// 否则关闭过程中会一边退出一边又接下新的活。
		if ctx.Err() != nil {
			return
		}

		j, err := w.store.Claim(ctx, w.id, w.lease)
		switch {
		case errors.Is(err, ErrNoJob):
			if !sleepOrDone(ctx, w.poll) {
				return
			}
			continue
		case err != nil:
			// 领取本身出错（库暂时不可用、连接被掐）。退避后重试，
			// 不退出：worker 退出会导致整个后台停下，而这通常只是暂时的。
			w.log.Warn("claim job failed", zap.Int("slot", slot), zap.Error(err))
			if !sleepOrDone(ctx, w.poll) {
				return
			}
			continue
		}

		w.execute(ctx, j, slot)
	}
}

// execute 执行一条任务并写回终态。
func (w *Worker) execute(ctx context.Context, j *Job, slot int) {
	// 每条任务一个可取消的上下文：取消信号来自两处——
	// 外层 ctx（进程要停了）和心跳发现租约丢失（这活已经不属于我）。
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 心跳发现租约丢失时要单独记一个标记，不能只看 ctx.Err()：
	// 进程停止同样会让 ctx 变成已取消，但两者该做的事完全不同——
	// 一个是"把任务放回队列"，一个是"什么都别写"（状态已被接管者改过）。
	var leaseLost bool
	var leaseMu sync.Mutex

	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		if w.heartbeat(runCtx, j.ID) {
			leaseMu.Lock()
			leaseLost = true
			leaseMu.Unlock()
			cancel() // 让 handler 尽快停下来
		}
	}()

	fn, ok := w.handlerFor(j.Kind)
	var runErr error
	if !ok {
		// 领到了没人认领的任务类型：多半是"旧代码在跑新任务"，属于部署问题。
		// 重试也没用，直接落 failed 并留下原因。
		runErr = fmt.Errorf("%w: %q", ErrUnsupportedKind, j.Kind)
	} else {
		runErr = fn(runCtx, j)
	}

	cancel() // 让心跳退出
	<-heartbeatDone

	leaseMu.Lock()
	lost := leaseLost
	leaseMu.Unlock()

	w.finish(ctx, j, slot, runErr, lost)
}

// heartbeat 周期性续约，返回是否为"租约已丢"。
//
// 续约失败分两种，处理方式不同：
//   - ErrLeaseLost：条件更新影响 0 行，说明任务已经不属于自己（被别人接管或已取消），
//     必须立刻停手，否则两个进程会同时写同一份数据。
//   - 其他错误（网络抖动、数据库瞬时不可用）：继续下一次心跳。
//     连续失败到租约过期时，数据库层面自然会把任务交给别人；
//     提前放弃反而会因为一次抖动丢掉正常任务。
func (w *Worker) heartbeat(ctx context.Context, id int64) bool {
	interval := w.lease / 3
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			err := w.store.RenewLease(ctx, id, w.id, w.lease)
			if err == nil {
				continue
			}
			if errors.Is(err, ErrLeaseLost) {
				w.log.Warn("job lease lost, aborting execution", zap.Int64("job_id", id))
				return true
			}
			w.log.Warn("renew job lease failed", zap.Int64("job_id", id), zap.Error(err))
		}
	}
}

// finish 写回终态。
//
// 三种结局：
//  1. 租约已丢 → 什么都不写。接管者会负责这条任务，我们写任何状态都是覆盖别人的结果。
//  2. 进程停止导致的中断 → 放回队列，且不消耗尝试次数（Release 会把 attempts 减回去）。
//  3. 正常成功/失败 → 落 succeeded / failed，失败带上错误码与原因。
func (w *Worker) finish(ctx context.Context, j *Job, slot int, runErr error, leaseLost bool) {
	if leaseLost {
		return
	}

	// 写状态时不能直接用外层 ctx：优雅停止的那一刻它已经取消了，
	// 拿它去 UPDATE 会立刻失败，任务就卡在 running 里等租约过期——
	// 那正是"优雅停止"要避免的情况。
	// context.WithoutCancel 保留原 ctx 的取值（追踪信息），去掉取消信号；
	// 再加超时兜底，避免数据库真的挂了时关闭流程无限等待。
	writeCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		writeCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}

	if runErr == nil {
		if err := w.store.Succeed(writeCtx, j.ID, w.id); err != nil {
			w.log.Warn("mark job succeeded failed", zap.Int64("job_id", j.ID), zap.Error(err))
		}
		return
	}

	if isShutdown(runErr, ctx) {
		if err := w.store.Release(writeCtx, j.ID, w.id); err != nil {
			w.log.Warn("release job on shutdown failed", zap.Int64("job_id", j.ID), zap.Error(err))
		}
		return
	}

	code := "handler_error"
	if errors.Is(runErr, ErrUnsupportedKind) {
		code = "unsupported_kind"
	}
	if err := w.store.Fail(writeCtx, j.ID, w.id, code, runErr.Error()); err != nil {
		w.log.Warn("mark job failed failed",
			zap.Int64("job_id", j.ID), zap.Int("slot", slot), zap.Error(err))
	}
}

// ErrUnsupportedKind 由 worker 在"没有注册 handler"时产生，
// 单独定义是为了让错误码可以被程序判断，而不是靠匹配错误文案。
var ErrUnsupportedKind = errors.New("no handler registered for kind")

// isShutdown 判断错误是不是"进程要停了"造成的。
//
// 用 errors.Is 而不是比较字符串：handler 可能把 ctx.Err() 包一层返回，
// 用文案匹配会在某次重构后静默失效，届时任务会被误判为失败。
func isShutdown(runErr error, parent context.Context) bool {
	return parent.Err() != nil && errors.Is(runErr, context.Canceled)
}

// sleepOrDone 睡一会儿，但可以被取消立刻打断。返回 false 表示该退出了。
// 直接用 time.Sleep 会让停止最多延迟一个轮询间隔，而且睡着的 goroutine 不响应取消。
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
