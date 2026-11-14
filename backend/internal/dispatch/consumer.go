// Package dispatch 把 outbox 消息分发给业务处理函数。
//
// 职责边界：只做"去重 → 路由 → 执行 → 失败重试 → 标记已消费"，
// 不认识文档、不认识数据库表结构。业务通过 topic 注册进来。
package dispatch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/platform"
)

// Handler 处理一条消息。返回 error 表示"这次没成功"，由 Consumer 决定是否重试。
type Handler func(ctx context.Context, env platform.Envelope) error

// Store 是 Consumer 需要的最小存储能力。
type Store interface {
	// Consumed 判断该 message_id 是否处理过。
	Consumed(ctx context.Context, messageID string) (bool, error)
	// MarkConsumed 记录处理完成（含"确定不重试"的放弃）。
	MarkConsumed(ctx context.Context, messageID, topic string) error
	// Retry 把原消息重新排队：status 回到待发送、available_at 推迟、attempts +1。
	//
	// 实现方应当**更新原行**而不是插入新行：message_id 保持稳定，
	// 消费端去重语义才正确（同一条任务重试几次都是同一条消息），
	// 表也不会因为重试而膨胀。历史留在 attempts 与 last_error 上。
	Retry(ctx context.Context, env platform.Envelope, delay time.Duration, reason string) error
}

// Consumer 按 topic 分发消息。
type Consumer struct {
	store    Store
	log      *zap.Logger
	registry map[string]Handler
	mu       sync.RWMutex

	// maxAttempts 是执行重试上限：超过就把消息标成已消费并记错误，
	// 避免一条坏消息被无限重投。
	maxAttempts int
}

// NewConsumer 组装。maxAttempts <= 0 时按 3 次处理。
func NewConsumer(store Store, log *zap.Logger, maxAttempts int) *Consumer {
	if log == nil {
		log = zap.NewNop()
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	return &Consumer{store: store, log: log, registry: map[string]Handler{}, maxAttempts: maxAttempts}
}

// Register 注册某个 topic 的处理函数。重复注册视为编码错误，直接 panic。
func (c *Consumer) Register(topic string, h Handler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.registry[topic]; exists {
		panic(fmt.Sprintf("dispatch: handler already registered for topic %q", topic))
	}
	c.registry[topic] = h
}

// Handle 处理单条消息。
//
// 返回 error 只用于日志：消息最终总会被 ack，是否重试由这里写新的 outbox 消息决定。
// 这样退避与次数都持久化在表里，broker 不需要延迟队列插件。
func (c *Consumer) Handle(ctx context.Context, env platform.Envelope) error {
	consumed, err := c.store.Consumed(ctx, env.MessageID)
	if err != nil {
		// 查不到去重表就别继续：重复执行比延迟执行危险得多。
		return fmt.Errorf("check consumed %s: %w", env.MessageID, err)
	}
	if consumed {
		c.log.Debug("skip duplicated message",
			zap.String("message_id", env.MessageID), zap.String("topic", env.Topic))
		return nil
	}

	c.mu.RLock()
	handler, ok := c.registry[env.Topic]
	c.mu.RUnlock()
	if !ok {
		// 未知 topic 多半是部署版本不一致（新消息发给了旧代码）。
		// 标成已消费而不是无限重投：重投也还是没人认领。
		c.log.Error("no handler for topic, dropping message",
			zap.String("topic", env.Topic), zap.String("message_id", env.MessageID))
		_ = c.store.MarkConsumed(ctx, env.MessageID, env.Topic)
		return ErrUnknownTopic
	}

	if err := handler(ctx, env); err != nil {
		// 次数来自信封（relay 从 outbox.attempts 填），不读 payload。
		attempt := env.Attempt + 1
		if attempt >= c.maxAttempts {
			c.log.Error("message failed after retries, giving up",
				zap.String("topic", env.Topic), zap.String("message_id", env.MessageID),
				zap.Int("attempt", attempt), zap.Error(err))
			_ = c.store.MarkConsumed(ctx, env.MessageID, env.Topic)
			return err
		}
		if rerr := c.store.Retry(ctx, env, retryBackoff(attempt), err.Error()); rerr != nil {
			c.log.Error("schedule retry failed, message dropped",
				zap.String("message_id", env.MessageID), zap.Error(rerr))
		}
		return err
	}

	if err := c.store.MarkConsumed(ctx, env.MessageID, env.Topic); err != nil {
		// 已经执行成功了；标记失败最多导致重复执行一次，
		// 处理函数本身幂等（写分块是一个事务）。
		c.log.Warn("mark consumed failed",
			zap.String("message_id", env.MessageID), zap.Error(err))
	}
	return nil
}

// ErrUnknownTopic 表示没有为该 topic 注册处理函数。
var ErrUnknownTopic = fmt.Errorf("dispatch: unknown topic")

// retryBackoff 与任务执行失败的退避保持一致：3s 起、指数增长、封顶 5 分钟。
func retryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 3 * time.Second << (attempt - 1)
	if delay > 5*time.Minute || delay <= 0 {
		return 5 * time.Minute
	}
	return delay
}
