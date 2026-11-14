// Package task 是"一次异步动作"的进度记录。
//
// 与 outbox 的分工是刻意的：
//   - task 回答"这件事办到哪了"，跨重启、要给页面查、要留历史；
//   - outbox 回答"消息送出去了没有"，发完就删。
//
// 把两者合成一张表（旧的 jobs 做法）会导致状态与投递互相污染：
// 清历史的操作会误删进度，重投消息要伪造进度字段。
package task

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Status 是任务进度。与迁移里的 CHECK 约束一致。
type Status string

const (
	// StatusPending 已受理，尚未开始执行。
	StatusPending Status = "pending"
	// StatusRunning 正在执行。
	StatusRunning Status = "running"
	// StatusSucceeded 执行成功。
	StatusSucceeded Status = "succeeded"
	// StatusFailed 执行失败且不再重试。
	StatusFailed Status = "failed"
	// StatusCancelled 被用户取消。
	StatusCancelled Status = "cancelled"
)

// Terminal 表示任务不会再变化，前端据此停止轮询。
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Task 是一次异步动作的进度记录。
type Task struct {
	ID         int64  `gorm:"primaryKey" json:"id"`
	Type       string `gorm:"type:varchar(64);not null" json:"type"`
	TargetType string `gorm:"type:varchar(32);not null" json:"target_type"`
	TargetID   int64  `gorm:"not null" json:"target_id"`

	Status   Status `gorm:"type:varchar(16);not null;default:pending" json:"status"`
	Attempts int    `gorm:"not null;default:0" json:"attempts"`

	ErrorCode    *string `gorm:"type:text" json:"error_code,omitempty"`
	ErrorMessage *string `gorm:"type:text" json:"error_message,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// TableName 写死表名，避免结构体改名时静默换表。
func (Task) TableName() string { return "tasks" }

// TargetDocument 是当前唯一的业务对象类型。
// 定义成常量而不是到处写 "document"：将来加 memory/agent 时只改这里能查全。
const TargetDocument = "document"

// New 构造一条待执行的任务。
func New(kind string, targetID int64) *Task {
	return &Task{
		Type:       kind,
		TargetType: TargetDocument,
		TargetID:   targetID,
		Status:     StatusPending,
	}
}

// AppendTx 用调用方的事务写入，让"业务变更"与"任务受理"同生共死。
//
// 返回时 t.ID 已由数据库自增填好，调用方可以直接把它写进 outbox 的 payload，
// 消费端就能凭 task_id 回表更新进度。
func AppendTx(_ context.Context, tx *gorm.DB, t *Task) error {
	if err := tx.Omit("created_at", "updated_at").Create(t).Error; err != nil {
		return fmt.Errorf("append task: %w", err)
	}
	return nil
}
