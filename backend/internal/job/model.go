// Package job 是后台任务的基础设施：任务的持久化、领取、续约、重试与取消。
//
// 设计边界（重要）：
//   - 这个包不认识 knowledge、memory、agent 任何一个业务模块。它只知道
//     "有一件事要做、谁在做、做到哪了"，具体做什么由使用方注册的 handler 决定。
//   - 依赖方向是业务 → job，反向不成立。这样 Phase 16 换成"MQ 唤醒 + 仍然落库"
//     时，只需要替换"谁把 worker 叫醒"，任务状态机一行都不用改。
//
// 与 outbox 的区别（很容易混）：
//   - outbox 记录"发生过什么，要告诉别人"，解决的是"库改了但消息没发出去"；
//   - jobs 记录"要做什么，做到哪了"，解决的是"这件事谁来做、崩了怎么接着做"。
package job

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Kind 是任务类型。取值由业务侧定义常量，本包不枚举——
// 新增一种任务不应该要求改基础设施。
type Kind string

// Status 是任务状态。与迁移里的 CHECK 约束一一对应，
// 两边必须同时改：约束负责"数据库里不可能有非法状态"，
// 常量负责"代码里不会拼错字符串"。
type Status string

const (
	// StatusQueued 待领取。run_after 到点后才会被领走。
	StatusQueued Status = "queued"
	// StatusRunning 已被某个 worker 领走，持有租约。
	StatusRunning Status = "running"
	// StatusSucceeded 成功结束。
	StatusSucceeded Status = "succeeded"
	// StatusFailed 失败且不再重试（不可恢复错误，或已达 max_attempts）。
	StatusFailed Status = "failed"
	// StatusCancelling 已受理取消请求，等 worker 感知后转 cancelled。
	// 单独一档是因为"取消"对运行中的任务只能是请求，不能立刻生效：
	// worker 可能正卡在一次解析里，只有它自己知道什么时候能安全停下。
	StatusCancelling Status = "cancelling"
	// StatusCancelled 已停止。
	StatusCancelled Status = "cancelled"
)

// Terminal 表示任务已经不会再变化。
//
// 前端据它决定"还要不要继续轮询"，补扫也据它排除已完成的行；
// 写在类型上而不是散在各处比较字符串，避免漏掉某个终态。
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Active 表示任务还占着"同一件工作只能有一个"的名额。
// 与迁移里部分唯一索引的 WHERE 条件保持一致。
func (s Status) Active() bool {
	return s == StatusQueued || s == StatusRunning
}

// Payload 是任务参数，落库为 jsonb。
//
// 为什么要自定义类型而不是直接用 []byte：
// GORM 会把 []byte 当成 bytea，写进 jsonb 列会报类型不匹配；
// 用 map[string]any 则既没有 Valuer 也没有 Scanner，同样读写不了。
// 这与 Phase 5 里 text[] 必须自定义 StringArray 是同一类问题——
// 遇到 PostgreSQL 的非标量类型，先想"驱动认不认这个 Go 类型"。
type Payload map[string]any

// Value 实现 driver.Valuer：写库时序列化成 JSON 文本。
func (p Payload) Value() (driver.Value, error) {
	if p == nil {
		// 空 map 而不是 NULL：读回来时调用方永远拿到可写的 map，少一处判空。
		return "{}", nil
	}
	// 先转成普通 map 再序列化：json.Marshal 能处理 map[string]any 与
	// 命名 map 类型，但显式转换能让"这就是 JSON 对象"这件事在代码里可读。
	raw, err := json.Marshal(map[string]any(p))
	if err != nil {
		return nil, fmt.Errorf("marshal job payload: %w", err)
	}
	return string(raw), nil
}

// Scan 实现 sql.Scanner：读库时反序列化。
// pgx 对 jsonb 通常给 []byte，但不排除驱动换成 string，两种都接。
func (p *Payload) Scan(src any) error {
	var raw []byte
	switch v := src.(type) {
	case nil:
		*p = Payload{}
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("scan job payload: unsupported type %T", src)
	}
	if len(raw) == 0 {
		*p = Payload{}
		return nil
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("unmarshal job payload: %w", err)
	}
	*p = decoded
	return nil
}

// Job 是 jobs 表的一行。
//
// 字段与迁移一一对应；指针类型用于"可以为 NULL"的列——
// 用零值代替会让"从未开始"和"1970 年开始"变成同一件事。
type Job struct {
	ID        int64   `gorm:"primaryKey" json:"id"`
	Kind      Kind    `gorm:"type:text;not null" json:"kind"`
	Status    Status  `gorm:"type:text;not null;default:queued" json:"status"`
	DedupeKey *string `gorm:"type:text" json:"dedupe_key,omitempty"`
	Payload   Payload `gorm:"type:jsonb;not null" json:"payload"`

	Attempts    int `gorm:"not null;default:0" json:"attempts"`
	MaxAttempts int `gorm:"not null;default:3" json:"max_attempts"`

	LastErrorCode *string `gorm:"type:text" json:"last_error_code,omitempty"`
	LastError     *string `gorm:"type:text" json:"last_error,omitempty"`

	RunAfter       time.Time  `gorm:"not null" json:"run_after"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	WorkerID       *string    `gorm:"type:text" json:"-"` // 内部排查用，不对外暴露

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// TableName 显式指定表名：GORM 默认会把 Job 复数化成 jobs，
// 但依赖默认值意味着改个结构体名就会静默换表，这里写死更稳。
func (Job) TableName() string { return "jobs" }

// DocumentID 从 payload 里取文档 id。
//
// JSON 反序列化后数字一定是 float64（encoding/json 的既有行为），
// 直接 `p["document_id"].(int64)` 会 panic——这是从 TS 转过来最容易踩的坑之一。
// 统一在这里做一次转换，业务 handler 就不用各自处理。
func (j Job) DocumentID() (int64, error) {
	raw, ok := j.Payload["document_id"]
	if !ok {
		return 0, fmt.Errorf("job %d payload has no document_id", j.ID)
	}
	switch v := raw.(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("job %d payload document_id has type %T", j.ID, raw)
	}
}
