// 本文件是 outbox 的数据访问层：只碰 SQL，不做投递决策。
package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
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
func (r *Repository) AppendTx(ctx context.Context, tx *gorm.DB, m *Message) error {
	return AppendTx(ctx, tx, m)
}

// AppendTx 是包级版本：业务仓储在自己的事务里直接写 outbox，
// 不必为了一个方法去构造 outbox.Repository。
func AppendTx(_ context.Context, tx *gorm.DB, m *Message) error {
	// message_id 由 Go 侧生成而不是交给数据库 DEFAULT：
	// 受理接口要把它返回给前端（"这条消息的编号"），
	// 只有插入时就已知才拿得到，数据库生成的值得再查一次。
	// 建表仍保留 DEFAULT gen_random_uuid()，供手工插入/迁移兜底。
	if m.MessageID == "" {
		m.MessageID = uuid.NewString()
	}
	if err := tx.Omit("created_at", "published_at").Create(m).Error; err != nil {
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

// RescheduleByMessageID 把"已投递但执行失败"的消息重新排队。
//
// 按 message_id 而不是主键定位：消费端手里只有信封，没有表内 id。
// 同时把 published_at 清空——不清的话它会被误判成"早已投递、无人认领"。
func (r *Repository) RescheduleByMessageID(ctx context.Context, messageID string, delay time.Duration, reason string) error {
	err := r.db.WithContext(ctx).Model(&Message{}).
		Where("message_id = ?", messageID).
		Updates(map[string]any{
			"status":       StatusPending,
			"attempts":     gorm.Expr("attempts + 1"),
			"available_at": time.Now().UTC().Add(delay),
			"last_error":   reason,
			"published_at": nil,
		}).Error
	if err != nil {
		return fmt.Errorf("reschedule outbox message %s: %w", messageID, err)
	}
	return nil
}

// PurgeSent 分批删除已投递且超过保留期的消息，返回删除条数。
//
// 为什么必须分批：一次删几万行会长时间持锁，期间 relay 的 Pending 查询会被拖慢。
// 每轮删一小批，删不完下一轮接着删，对线上几乎无感。
//
// 只删 status=1（已投递）：status=0 还在排队，status=2 是投递失败等人看的，
// 两者都不能自动消失。
func (r *Repository) PurgeSent(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	var ids []int64
	if err := r.db.WithContext(ctx).Model(&Message{}).
		Where("status = ? AND published_at IS NOT NULL AND published_at < ?", StatusSent, before).
		Order("id").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, fmt.Errorf("list expired outbox messages: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).Where("id IN ?", ids).Delete(&Message{})
	if result.Error != nil {
		return 0, fmt.Errorf("purge outbox messages: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// PurgeConsumed 分批删除过期的去重记录。
//
// 去重表只为"消息重投时不重复执行"存在，保留期比 outbox 更短即可：
// 消息本身在 outbox 里留 7 天，重投不可能发生在它被删之后。
func (r *Repository) PurgeConsumed(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	// 用子查询而不是 IN：这张表的主键是 uuid，先取出来再删要多一次往返。
	result := r.db.WithContext(ctx).Exec(`
DELETE FROM consumed_messages
WHERE message_id IN (
    SELECT message_id FROM consumed_messages WHERE consumed_at < ? LIMIT ?
)`, before, limit)
	if result.Error != nil {
		return 0, fmt.Errorf("purge consumed messages: %w", result.Error)
	}
	return result.RowsAffected, nil
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
