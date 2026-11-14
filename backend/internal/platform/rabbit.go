// 本文件是 RabbitMQ 的最小封装：连接、声明队列、发布、消费。
//
// 定位先说清楚：**队列只负责"叫醒"，任务状态仍然在 jobs 表里。**
// 所以这里不做 publisher confirm、不做死信、不做延迟队列——
// 消息丢了有补扫兜底，失败重试由表里的 run_after 驱动。
// 一旦把状态也放进 MQ（ack 即状态），取消、进度、失败原因都得另找地方存。
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

// ErrQueueClosed 表示连接或 channel 已经关闭，调用方应当重建。
var ErrQueueClosed = errors.New("rabbitmq: connection closed")

// TaskEnvelope 是旧版消息体（只带 job_id），保留给仍在轮询的 worker。
type TaskEnvelope struct {
	JobID int64 `json:"job_id"`
}

// Envelope 是 outbox 消息的完整信封。
//
// 与旧 TaskEnvelope 的区别：消息自带 message_id（消费端去重）、topic（路由）
// 与 msg_key（保序），消费端不再需要回表取这些元信息。
type Envelope struct {
	MessageID string          `json:"message_id"`
	Topic     string          `json:"topic"`
	MsgKey    string          `json:"msg_key"`
	Payload   json.RawMessage `json:"payload"`
	// Attempt 是"第几次投递"，由 relay 从 outbox.attempts 填入。
	// 放在信封上而不是 payload 里：它是消息元数据，业务参数不该被塞进重试计数。
	Attempt int `json:"attempt"`
}

// PublishEnvelope 投递一条 outbox 消息。
//
// 用 MessageId 作为 AMQP 层的消息 id：broker 侧可见，消费端也用同一个值去重，
// 排查"这条消息为什么被跳过"时不必两边对号。
func (r *Rabbit) PublishEnvelope(ctx context.Context, env Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrQueueClosed
	}
	return r.channel.PublishWithContext(ctx, "", r.opts.Queue, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    env.MessageID,
		Timestamp:    time.Now().UTC(),
		Body:         body,
	})
}

// ConsumeEnvelopes 阻塞消费信封格式的消息，直到 ctx 取消或连接断开。
//
// ack 语义与旧的 Consume 相同：**一律 ack**。重试不是靠 nack 重投，
// 而是由消费端在失败时写一条新的 outbox 消息（新的 message_id、推迟的 available_at），
// 这样退避与次数都能被持久化，broker 侧不需要延迟队列插件。
func (r *Rabbit) ConsumeEnvelopes(ctx context.Context, handle func(context.Context, Envelope) error) error {
	ch, err := r.conn.Channel()
	if err != nil {
		return fmt.Errorf("open consumer channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	if err := ch.Qos(r.opts.Prefetch, 0, false); err != nil {
		return fmt.Errorf("set qos: %w", err)
	}
	deliveries, err := ch.Consume(r.opts.Queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %q: %w", r.opts.Queue, err)
	}

	r.log.Info("outbox consumer started",
		zap.String("queue", r.opts.Queue), zap.Int("prefetch", r.opts.Prefetch))

	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return ErrQueueClosed
			}
			var env Envelope
			if err := json.Unmarshal(d.Body, &env); err != nil || env.MessageID == "" {
				r.log.Warn("dropping malformed envelope", zap.Int("body_len", len(d.Body)), zap.Error(err))
				_ = d.Reject(false)
				continue
			}
			if err := handle(ctx, env); err != nil {
				r.log.Warn("handle envelope failed",
					zap.String("message_id", env.MessageID), zap.String("topic", env.Topic), zap.Error(err))
			}
			if err := d.Ack(false); err != nil {
				r.log.Warn("ack envelope failed", zap.String("message_id", env.MessageID), zap.Error(err))
			}
		}
	}
}

// RabbitOptions 是连接与拓扑参数。
type RabbitOptions struct {
	URL      string
	Queue    string
	Prefetch int
}

// Rabbit 持有长连接与一条发布 channel。
//
// 为什么发布 channel 要单独一条：消费 channel 是阻塞读取的，共用一个 channel
// 会让发布和消费互相卡住（amqp091 的 channel 不是并发安全的）。
type Rabbit struct {
	conn    *amqp.Connection
	channel *amqp.Channel
	opts    RabbitOptions
	log     *zap.Logger

	mu     sync.Mutex
	closed bool
}

// OpenRabbit 建立连接并声明队列。
//
// 队列声明成 durable：broker 重启后队列还在。消息用 Persistent 投递模式，
// 但**不依赖它保证不丢**——真正的保险是表里的 queued 行加补扫。
func OpenRabbit(opts RabbitOptions, log *zap.Logger) (*Rabbit, error) {
	if log == nil {
		log = zap.NewNop()
	}
	conn, err := amqp.DialConfig(opts.URL, amqp.Config{
		// 心跳必须开：NAT 或闲置连接会被中间设备静默丢弃，
		// 没有心跳时客户端要等到下一次 publish 才发现连接已死。
		Heartbeat: 10 * time.Second,
		Locale:    "en_US",
	})
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}

	// 直接投递到默认交换机（名字为空），routing key 就是队列名。
	// 一条队列不需要额外的 exchange：多一个交换机只是多一处要维护的路由规则。
	if _, err := ch.QueueDeclare(opts.Queue, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("declare queue %q: %w", opts.Queue, err)
	}

	return &Rabbit{conn: conn, channel: ch, opts: opts, log: log}, nil
}

// Publish 投递一条"有新任务"的消息。
//
// 返回错误时调用方**不需要回滚业务事务**：任务已经在表里，补扫会重投。
// 这正是"表是真相、消息只是提醒"的好处——投递失败退化成延迟，不是丢数据。
func (r *Rabbit) Publish(ctx context.Context, jobID int64) error {
	body, err := json.Marshal(TaskEnvelope{JobID: jobID})
	if err != nil {
		return fmt.Errorf("marshal task envelope: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrQueueClosed
	}
	return r.channel.PublishWithContext(ctx, "", r.opts.Queue, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    strconv.FormatInt(jobID, 10),
		Timestamp:    time.Now().UTC(),
		Body:         body,
	})
}

// Consume 阻塞消费，直到 ctx 取消或连接断开（返回错误）。
//
// ack 策略：**一律 ack**，包括 handler 失败的情况。原因不是偷懒——
// handler 失败时任务状态与退避时间已经写进表里，重投只会让同一个 id
// 反复被领走；而真正需要"再来一次"的情形由补扫按 run_after 重新发布。
// 毒消息（消息体解析不出来）同理：nack 重投会无限循环。
func (r *Rabbit) Consume(ctx context.Context, handle func(context.Context, int64) error) error {
	ch, err := r.conn.Channel()
	if err != nil {
		return fmt.Errorf("open consumer channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	if err := ch.Qos(r.opts.Prefetch, 0, false); err != nil {
		return fmt.Errorf("set qos: %w", err)
	}

	deliveries, err := ch.Consume(r.opts.Queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %q: %w", r.opts.Queue, err)
	}

	r.log.Info("queue consumer started",
		zap.String("queue", r.opts.Queue), zap.Int("prefetch", r.opts.Prefetch))

	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				// channel 被关闭（broker 重启、网络中断），交给上层重连。
				return ErrQueueClosed
			}

			var env TaskEnvelope
			if err := json.Unmarshal(d.Body, &env); err != nil || env.JobID == 0 {
				r.log.Warn("dropping malformed job message",
					zap.Int("body_len", len(d.Body)), zap.Error(err))
				_ = d.Reject(false)
				continue
			}

			if err := handle(ctx, env.JobID); err != nil {
				// 注意：这里记日志而不是 nack。重试由表驱动，见函数注释。
				r.log.Warn("handle job message failed",
					zap.Int64("job_id", env.JobID), zap.Error(err))
			}
			if err := d.Ack(false); err != nil {
				r.log.Warn("ack job message failed",
					zap.Int64("job_id", env.JobID), zap.Error(err))
			}
		}
	}
}

// Healthy 报告连接是否还活着，供 /readyz 使用。
func (r *Rabbit) Healthy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.closed && r.conn != nil && !r.conn.IsClosed()
}

// Close 关闭 channel 与连接。可重复调用。
func (r *Rabbit) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.channel != nil {
		_ = r.channel.Close()
	}
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}
