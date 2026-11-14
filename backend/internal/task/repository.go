// 本文件是任务进度的读写：状态只能由消费者改。
package task

import (
	"context"
	"errors"
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

// ErrNotFound 表示该业务对象当前没有未完成的任务。
var ErrNotFound = errors.New("task not found")

// Active 返回某个业务对象当前未完成的任务（pending 或 running）。
//
// 取消处理要用它判断"任务排着队还是在跑"：
//   - pending → 消费者还没抢到，可以直接取消；
//   - running → 只能标记取消意图，等执行方自己停下。
//
// 依赖部分唯一索引 tasks_active_uniq 保证最多返回一条。
func (r *Repository) Active(ctx context.Context, targetType string, targetID int64) (*Task, error) {
	var t Task
	err := r.db.WithContext(ctx).
		Where("target_type = ? AND target_id = ? AND status IN ?",
			targetType, targetID, []Status{StatusPending, StatusRunning}).
		Order("id DESC").First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("active task for %s %d: %w", targetType, targetID, err)
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

// CancelPending 只在任务还没被领走时取消它，并返回是否取消成功。
//
// 用条件更新而不是先读后写：读完 Active 到真正执行之间有窗口，
// 消费端可能刚好把 pending 推成 running。那时返回 false，
// 调用方改走"标记取消中"的路径，避免出现"任务已取消、文档却被写成 ready"。
func (r *Repository) CancelPending(ctx context.Context, id int64) (bool, error) {
	result := r.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status = ?", id, StatusPending).
		Updates(map[string]any{
			"status":        StatusCancelled,
			"error_code":    "cancelled",
			"error_message": "处理已取消",
			"updated_at":    time.Now().UTC(),
			"finished_at":   time.Now().UTC(),
		})
	if result.Error != nil {
		return false, fmt.Errorf("cancel pending task %d: %w", id, result.Error)
	}
	return result.RowsAffected > 0, nil
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
