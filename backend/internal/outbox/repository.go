// 本文件是 outbox 的数据访问层：只碰 SQL，不做投递决策。
package outbox

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Repository 是 outbox 表的读写入口。
type Repository struct {
	db *gorm.DB
}

// NewRepository 构造。
func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

// Append 写入一条待发送消息。
//
// 业务在自己的事务里调用它（通过 AppendTx），保证"业务变更"与"要发消息"
// 要么一起提交、要么一起回滚——这正是 outbox 存在的理由。
func (r *Repository) Append(ctx context.Context, m *Message) error {
	return r.AppendTx(ctx, r.db.WithContext(ctx), m)
}

// AppendTx 用调用方的事务写入，供业务层插入。
func (r *Repository) AppendTx(_ context.Context, tx *gorm.DB, m *Message) error {
	// Omit 掉由数据库生成的列：message_id 有 DEFAULT gen_random_uuid()，
	// 从 Go 侧传零值进去会写入空 UUID（类型能通过，但失去唯一性语义）。
	if err := tx.Omit("message_id", "created_at", "published_at").Create(m).Error; err != nil {
		return fmt.Errorf("append outbox message: %w", err)
	}
	return nil
}

// Pending 取出到点待发送的消息。
//
// 不加行锁：relay 单实例跑，重复投递由消费端按 message_id 去重兜住。
// 加锁反而会在 relay 重启时留一堆锁等待。
func (r *Repository) Pending(ctx context.Context, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var msgs []Message
	err := r.db.WithContext(ctx).
		Where("status = ? AND available_at <= now()", StatusPending).
		Order("available_at, id").
		Limit(limit).
		Find(&msgs).Error
	if err != nil {
		return nil, fmt.Errorf("list pending outbox messages: %w", err)
	}
	return msgs, nil
}

// MarkSent 记录投递成功。
func (r *Repository) MarkSent(ctx context.Context, id int64) error {
	err := r.db.WithContext(ctx).Model(&Message{}).Where("id = ?", id).
		Updates(map[string]any{
			"status":       StatusSent,
			"published_at": time.Now().UTC(),
			"last_error":   nil,
		}).Error
	if err != nil {
		return fmt.Errorf("mark outbox message %d sent: %w", id, err)
	}
	return nil
}

// Reschedule 投递失败后重新排队：次数 +1、推迟到 delay 之后、记下原因。
func (r *Repository) Reschedule(ctx context.Context, id int64, delay time.Duration, reason string) error {
	err := r.db.WithContext(ctx).Model(&Message{}).Where("id = ?", id).
		Updates(map[string]any{
			"status":       StatusPending,
			"attempts":     gorm.Expr("attempts + 1"),
			"available_at": time.Now().UTC().Add(delay),
			"last_error":   reason,
		}).Error
	if err != nil {
		return fmt.Errorf("reschedule outbox message %d: %w", id, err)
	}
	return nil
}

// GiveUp 把消息标成失败：重试次数用尽，需要人工看过。
func (r *Repository) GiveUp(ctx context.Context, id int64, reason string) error {
	err := r.db.WithContext(ctx).Model(&Message{}).Where("id = ?", id).
		Updates(map[string]any{
			"status":     StatusFailed,
			"attempts":   gorm.Expr("attempts + 1"),
			"last_error": reason,
		}).Error
	if err != nil {
		return fmt.Errorf("give up outbox message %d: %w", id, err)
	}
	return nil
}

// RecentlyFor 按业务 key 取最近的若干条消息，供页面展示进度与失败原因。
func (r *Repository) RecentlyFor(ctx context.Context, msgKey string, limit int) ([]Message, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	var msgs []Message
	err := r.db.WithContext(ctx).
		Where("msg_key = ?", msgKey).
		Order("created_at DESC").
		Limit(limit).
		Find(&msgs).Error
	if err != nil {
		return nil, fmt.Errorf("list outbox messages for %s: %w", msgKey, err)
	}
	return msgs, nil
}

// Consumed 判断某条消息是否已经处理过（消费端去重）。
func (r *Repository) Consumed(ctx context.Context, messageID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("consumed_messages").
		Where("message_id = ?", messageID).Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check consumed message %s: %w", messageID, err)
	}
	return count > 0, nil
}

// MarkConsumed 记录"这条消息处理过了"。
//
// 用 ON CONFLICT DO NOTHING：并发重复消费时不该报错，
// 唯一主键本身就表达了"只记一次"。
func (r *Repository) MarkConsumed(ctx context.Context, messageID, topic string) error {
	err := r.db.WithContext(ctx).Exec(
		`INSERT INTO consumed_messages (message_id, topic) VALUES (?, ?) ON CONFLICT DO NOTHING`,
		messageID, topic).Error
	if err != nil {
		return fmt.Errorf("mark message %s consumed: %w", messageID, err)
	}
	return nil
}
