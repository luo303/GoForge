// 本文件是 relay：把 outbox 里待发送的消息投递到 broker。
//
// 它是"事务"与"消息队列"之间唯一的那段胶水。没有它，业务就得在
// "提交事务"和"发消息"之间做选择，两者必丢其一。
package outbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// ErrQueueUnavailable 表示投递通道本身不可用（连接没建起来、broker 挂了）。
//
// 必须与"这条消息发不出去"区分开：通道不可用时如果照常消耗重试次数，
// 一次几十秒的 broker 抖动就能把攒了几分钟的消息全部打成"放弃"，
// 而且这些消息再也不会被自动重试。
var ErrQueueUnavailable = errors.New("outbox: queue unavailable")

// queueUnavailableRetry 是通道不可用时的重试间隔。
//
// 比正常轮询（200ms）长得多：没有通道，转得再快也发不出去，
// 只会把日志刷满。5 秒足够在 broker 恢复后很快接上。
const queueUnavailableRetry = 5 * time.Second

// store 是 relay 需要的仓储能力，定义在消费方便于测试替身。
type store interface {
	Pending(ctx context.Context, limit int) ([]Message, error)
	MarkSent(ctx context.Context, id int64) error
	Reschedule(ctx context.Context, id int64, delay time.Duration, reason string) error
	GiveUp(ctx context.Context, id int64, reason string) error
}

// Queue 是投递能力，由 platform.Rabbit 实现。
type Queue interface {
	Publish(ctx context.Context, m Message) error
}

// RelayOptions 是 relay 的运行参数。
type RelayOptions struct {
	// Batch 是单轮最多处理多少条：积压时批量发，空闲时也就查一次。
	Batch int
	// Interval 是没活干时的轮询间隔。与旧的 worker 轮询不同，
	// 这里查的是"未发送的消息"，正常情况下一张空表，代价极低。
	Interval time.Duration
	// MaxAttempts 是投递重试上限，超过就标记失败等人处理。
	MaxAttempts int
}

const (
	defaultRelayBatch       = 50
	defaultRelayInterval    = 200 * time.Millisecond
	defaultRelayMaxAttempts = 10
	maxRelayBackoff         = time.Minute
)

// Relay 周期性地把待发送消息推给 broker。
type Relay struct {
	store store
	queue Queue
	log   *zap.Logger
	opts  RelayOptions
}

// NewRelay 组装。queue 为 nil 时不启动（broker 不可用属于可降级状态）。
func NewRelay(s store, queue Queue, log *zap.Logger, opts RelayOptions) *Relay {
	if log == nil {
		log = zap.NewNop()
	}
	if opts.Batch <= 0 {
		opts.Batch = defaultRelayBatch
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultRelayInterval
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = defaultRelayMaxAttempts
	}
	return &Relay{store: s, queue: queue, log: log, opts: opts}
}

// Run 阻塞运行直到 ctx 取消。启动时先立刻跑一轮，避免等一个 interval。
func (r *Relay) Run(ctx context.Context) {
	if r == nil || r.queue == nil {
		return
	}
	ticker := time.NewTicker(r.opts.Interval)
	defer ticker.Stop()

	for {
		if _, err := r.Drain(ctx); err != nil && ctx.Err() == nil {
			if errors.Is(err, ErrQueueUnavailable) {
				// 没有通道时不必按正常节奏重试，也不必每轮记一条 warn：
				// 广播一条日志，然后按 5 秒节奏等 broker 回来。
				r.log.Warn("outbox relay paused: queue unavailable", zap.Error(err))
				select {
				case <-ctx.Done():
					return
				case <-time.After(queueUnavailableRetry):
				}
				continue
			}
			r.log.Warn("outbox relay drain failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Drain 处理一批待发送消息，返回成功投递的条数。
//
// 单条失败不影响同批其它消息：否则一条坏消息会把整批卡住。
func (r *Relay) Drain(ctx context.Context) (int, error) {
	if r == nil || r.queue == nil {
		return 0, nil
	}
	msgs, err := r.store.Pending(ctx, r.opts.Batch)
	if err != nil {
		return 0, err
	}

	sent := 0
	for i := range msgs {
		m := &msgs[i]
		if err := r.queue.Publish(ctx, *m); err != nil {
			// 通道不可用：这一批都发不出去，没必要逐条试、更不能逐条消耗重试次数。
			// 直接返回，由 Run 降频等待。消息保持"待发"，什么都没变。
			if errors.Is(err, ErrQueueUnavailable) {
				return sent, fmt.Errorf("%w: %v", ErrQueueUnavailable, err)
			}
			// 投递失败：还有次数就退避重排，否则标死。
			// 这里不改业务实体状态——任务没发出去，业务本来就还没开始执行。
			next := m.Attempts + 1
			if next >= r.opts.MaxAttempts {
				if gerr := r.store.GiveUp(ctx, m.ID, err.Error()); gerr != nil {
					r.log.Warn("give up outbox message failed",
						zap.Int64("outbox_id", m.ID), zap.Error(gerr))
				}
				r.log.Error("outbox message gave up",
					zap.Int64("outbox_id", m.ID), zap.String("topic", m.Topic), zap.Error(err))
				continue
			}
			if rerr := r.store.Reschedule(ctx, m.ID, relayBackoff(next), err.Error()); rerr != nil {
				r.log.Warn("reschedule outbox message failed",
					zap.Int64("outbox_id", m.ID), zap.Error(rerr))
			}
			continue
		}

		if err := r.store.MarkSent(ctx, m.ID); err != nil {
			// 消息已经发出去了，标记失败只会导致下一轮重发；
			// 消费端按 message_id 去重，重复投递无害。
			r.log.Warn("mark outbox message sent failed",
				zap.Int64("outbox_id", m.ID), zap.Error(err))
		}
		sent++
	}
	return sent, nil
}

// relayBackoff 是投递失败的退避：3s 起步、指数增长、封顶 1 分钟。
//
// 比任务执行失败（5 分钟封顶）短得多：投递失败几乎都是 broker 短暂不可用，
// 恢复后应当尽快补上；而且 relay 每 200ms 就会再看一眼，不差这点等待。
func relayBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := 3 * time.Second << (attempts - 1)
	if delay > maxRelayBackoff || delay <= 0 {
		return maxRelayBackoff
	}
	return delay
}
