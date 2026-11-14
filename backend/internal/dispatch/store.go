// 本文件把 dispatch.Store 落到 outbox 表上。
//
// 消费端需要的三件事（去重查询、记录已消费、重试排期）全部落在 outbox，
// 不需要消费端认识 outbox 这个包——它只看到一个三方法的接口。
package dispatch

import (
	"context"
	"time"

	"github.com/luo303/mindos/backend/internal/outbox"
	"github.com/luo303/mindos/backend/internal/platform"
)

// SQLStore 用 outbox 表实现 dispatch.Store。
type SQLStore struct {
	Outbox *outbox.Repository
}

// NewSQLStore 构造。
func NewSQLStore(repo *outbox.Repository) *SQLStore { return &SQLStore{Outbox: repo} }

// Consumed 查询 message_id 是否处理过。
func (s *SQLStore) Consumed(ctx context.Context, messageID string) (bool, error) {
	return s.Outbox.Consumed(ctx, messageID)
}

// MarkConsumed 记录处理完成。
func (s *SQLStore) MarkConsumed(ctx context.Context, messageID, topic string) error {
	return s.Outbox.MarkConsumed(ctx, messageID, topic)
}

// Retry 更新原消息让它重新排队（message_id 不变）。
func (s *SQLStore) Retry(ctx context.Context, env platform.Envelope, delay time.Duration, reason string) error {
	return s.Outbox.RescheduleByMessageID(ctx, env.MessageID, delay, reason)
}
