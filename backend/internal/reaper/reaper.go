// Package reaper 是任务链路的兜底：把"卡住"的处理重新推起来，
// 把走到生命尽头的消息清掉。
//
// 为什么 MQ 之外还需要它：消息一旦被 broker 投出并 ack，就不再重投了。
// 消费端在执行到一半崩掉时，tasks 会永远停在 running，而队列里已经没有
// 任何东西能唤醒它。outbox 可以随便删，正是因为真相在 tasks 这边——
// 但真相需要有东西去推动它。
package reaper

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/luo303/mindos/backend/internal/outbox"
	"github.com/luo303/mindos/backend/internal/task"
)

// Options 是兜底扫描的运行参数。
type Options struct {
	// Interval 是扫描间隔。它只是保底，正常情况下一轮查不出任何东西。
	Interval time.Duration
	// StaleAfter 是"多久没动过算卡住"。
	//
	// 不能太短：一次正常的文档处理可能跑十几秒，扫描抢在前面会重复投递
	// （虽然有 MarkRunning 的幂等闸门，但会白占一次执行名额）。
	StaleAfter time.Duration
	// Retention 是已投递消息的保留期，也是去重记录的保留期。
	Retention time.Duration
	// Batch 是单轮最多处理的条数。
	Batch int
}

const (
	defaultInterval   = 30 * time.Second
	defaultStaleAfter = 2 * time.Minute
	defaultRetention  = 7 * 24 * time.Hour
	defaultBatch      = 100
)

// Reaper 周期性地自愈与清理。
type Reaper struct {
	db     *gorm.DB
	tasks  *task.Repository
	outbox *outbox.Repository
	log    *zap.Logger
	opts   Options
}

// New 组装。零值参数按默认值处理。
func New(db *gorm.DB, tasks *task.Repository, ob *outbox.Repository, log *zap.Logger, opts Options) *Reaper {
	if log == nil {
		log = zap.NewNop()
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = defaultStaleAfter
	}
	if opts.Retention <= 0 {
		opts.Retention = defaultRetention
	}
	if opts.Batch <= 0 {
		opts.Batch = defaultBatch
	}
	return &Reaper{db: db, tasks: tasks, outbox: ob, log: log, opts: opts}
}

// Run 阻塞到 ctx 取消，期间按间隔执行。
//
// 启动先跑一次：上一任进程很可能就是在这里崩的，让积压尽早被处理，
// 而不是再等一个完整间隔。
func (r *Reaper) Run(ctx context.Context) {
	r.once(ctx)
	ticker := time.NewTicker(r.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.once(ctx)
		}
	}
}

func (r *Reaper) once(ctx context.Context) {
	r.requeueStuck(ctx)
	r.purge(ctx)
}

// stuckTask 是"卡住"的任务快照。
type stuckTask struct {
	ID         int64
	Type       string
	TargetType string
	TargetID   int64
}

// requeueStuck 找出卡住的任务并为每条重新投一条消息。
//
// 两个条件缺一不可：
//   - 状态还在 pending / running，且 updated_at 很久没动；
//   - outbox 里没有它的待发消息（有的话 relay 会处理，再投一条就是重复）。
//
// 这里直接写跨两表的 SQL 而不是拼两个仓储的调用：判断"卡住"本身
// 就依赖两张表的联合状态，拆成两次查询会出现"查完任务、投递前另一条消息到了"的窗口。
func (r *Reaper) requeueStuck(ctx context.Context) {
	var stuck []stuckTask
	err := r.db.WithContext(ctx).Raw(`
SELECT t.id, t.type, t.target_type, t.target_id
FROM tasks t
WHERE t.status IN ('pending', 'running')
  AND t.updated_at < now() - make_interval(secs => ?)
  AND NOT EXISTS (
      SELECT 1 FROM outbox o
      WHERE o.msg_key = t.target_id::text AND o.status = 0
  )
ORDER BY t.id
LIMIT ?`, r.opts.StaleAfter.Seconds(), r.opts.Batch).Scan(&stuck).Error
	if err != nil {
		r.log.Warn("scan stuck tasks failed", zap.Error(err))
		return
	}

	for _, t := range stuck {
		if t.TargetType != task.TargetDocument {
			// 目前只有文档主题。将来加别的对象类型时，重投的 payload 形状不同，
			// 宁可跳过并留一条日志，也不要发一条消费端看不懂的消息。
			r.log.Warn("skip stuck task of unknown target type",
				zap.Int64("task_id", t.ID), zap.String("target_type", t.TargetType))
			continue
		}

		// 先回 pending，让 MarkRunning 的条件更新可能成功：
		// 停在 running 的任务即使消息重投了，消费端也会因为"抢不到"直接跳过。
		if err := r.tasks.MarkPending(ctx, t.ID); err != nil {
			r.log.Warn("reset stuck task failed", zap.Int64("task_id", t.ID), zap.Error(err))
			continue
		}

		msg := outbox.New(t.Type, strconv.FormatInt(t.TargetID, 10), map[string]any{
			"document_id": t.TargetID,
			"task_id":     t.ID,
		})
		if err := r.outbox.Append(ctx, msg); err != nil {
			r.log.Warn("requeue stuck task failed",
				zap.Int64("task_id", t.ID), zap.Error(err))
			continue
		}
		r.log.Info("requeued stuck task",
			zap.Int64("task_id", t.ID), zap.String("type", t.Type),
			zap.String("message_id", msg.MessageID))
	}
}

// purge 清理超过保留期的历史，两处都分批。
func (r *Reaper) purge(ctx context.Context) {
	before := time.Now().Add(-r.opts.Retention)

	sent, err := r.outbox.PurgeSent(ctx, before, r.opts.Batch)
	if err != nil {
		r.log.Warn("purge sent outbox messages failed", zap.Error(err))
	} else if sent > 0 {
		r.log.Info("purged outbox messages", zap.Int64("count", sent))
	}

	consumed, err := r.outbox.PurgeConsumed(ctx, before, r.opts.Batch)
	if err != nil {
		r.log.Warn("purge consumed messages failed", zap.Error(err))
	} else if consumed > 0 {
		r.log.Info("purged consumed messages", zap.Int64("count", consumed))
	}
}

// String 让日志里能打出扫描参数，排查"为什么我的任务没被重投"时有用。
func (r *Reaper) String() string {
	return fmt.Sprintf("reaper(interval=%s stale=%s retention=%s)",
		r.opts.Interval, r.opts.StaleAfter, r.opts.Retention)
}
