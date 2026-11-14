// 本文件是任务进度的读写：状态只能由消费者改。
package task

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Repository 是 tasks 表的入口。
type Repository struct {
	db *gorm.DB
}

// NewRepository 构造。
func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

// Get 按主键读取任务。
func (r *Repository) Get(ctx context.Context, id int64) (*Task, error) {
	var t Task
	if err := r.db.WithContext(ctx).First(&t, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("get task %d: %w", id, err)
	}
	return &t, nil
}

// MarkRunning 把任务从 pending 推进到 running，并返回是否推进成功。
//
// 用条件更新而不是先读后写：重复投递的消息会并发到达，
// 只有第一个能把 pending 改成 running，其余拿到 false 直接跳过。
// 这就是"至少一次投递"下的执行幂等闸门。
func (r *Repository) MarkRunning(ctx context.Context, id int64) (bool, error) {
	result := r.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status = ?", id, StatusPending).
		Updates(map[string]any{
			"status":     StatusRunning,
			"started_at": time.Now().UTC(),
			"updated_at": time.Now().UTC(),
			"attempts":   gorm.Expr("attempts + 1"),
		})
	if result.Error != nil {
		return false, fmt.Errorf("mark task %d running: %w", id, result.Error)
	}
	return result.RowsAffected > 0, nil
}

// MarkPending 把任务放回待执行，供"可重试的失败"使用。
//
// 不放回 pending 的话，下一次投递会因为 MarkRunning 只接受 pending 而被跳过，
// 任务会永远停在 running。
func (r *Repository) MarkPending(ctx context.Context, id int64) error {
	err := r.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Updates(map[string]any{
			"status":     StatusPending,
			"updated_at": time.Now().UTC(),
		}).Error
	if err != nil {
		return fmt.Errorf("reset task %d to pending: %w", id, err)
	}
	return nil
}

// MarkSucceeded 写入成功终态。
func (r *Repository) MarkSucceeded(ctx context.Context, id int64) error {
	return r.finish(ctx, id, StatusSucceeded, nil, nil)
}

// MarkFailed 写入失败终态与原因。
func (r *Repository) MarkFailed(ctx context.Context, id int64, code, message string) error {
	return r.finish(ctx, id, StatusFailed, &code, &message)
}

// MarkCancelled 写入取消终态。
func (r *Repository) MarkCancelled(ctx context.Context, id int64) error {
	return r.finish(ctx, id, StatusCancelled, strPtr("cancelled"), strPtr("处理已取消"))
}

func (r *Repository) finish(ctx context.Context, id int64, status Status, code, message *string) error {
	err := r.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Updates(map[string]any{
			"status":        status,
			"error_code":    code,
			"error_message": message,
			"updated_at":    time.Now().UTC(),
			"finished_at":   time.Now().UTC(),
		}).Error
	if err != nil {
		return fmt.Errorf("finish task %d as %s: %w", id, status, err)
	}
	return nil
}

func strPtr(s string) *string { return &s }
