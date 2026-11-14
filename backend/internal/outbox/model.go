// Package outbox 实现"事务内写消息、事务外可靠投递"。
//
// 与旧的 jobs 表设计的关键区别：这张表只负责**把消息送出去**，
// 不再承担"任务状态机"。执行进度落在业务实体（documents.status）上，
// 这样消息与任务各归各的地方，不需要一张表同时扮两个角色。
package outbox

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Status 是投递状态。与迁移里的 CHECK 约束保持一致。
type Status int16

const (
	// StatusPending 待发送（含退避到期前）。
	StatusPending Status = 0
	// StatusSent 已投递到 broker。
	StatusSent Status = 1
	// StatusFailed 重试次数用尽，放弃。
	StatusFailed Status = 2
)

// Payload 是消息内容，落库为 jsonb。
//
// 自定义类型的原因与旧实现相同：GORM 把 []byte 当 bytea，
// map[string]any 又没有 Valuer/Scanner，读写 jsonb 都会失败。
type Payload map[string]any

// Value 实现 driver.Valuer。
func (p Payload) Value() (driver.Value, error) {
	if p == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(map[string]any(p))
	if err != nil {
		return nil, fmt.Errorf("marshal outbox payload: %w", err)
	}
	return string(raw), nil
}

// Scan 实现 sql.Scanner。
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
		return fmt.Errorf("scan outbox payload: unsupported type %T", src)
	}
	if len(raw) == 0 {
		*p = Payload{}
		return nil
	}
	return json.Unmarshal(raw, (*map[string]any)(p))
}

// Message 是一条待投递/已投递的消息。
type Message struct {
	ID        int64  `gorm:"primaryKey" json:"id"`
	MessageID string `gorm:"type:uuid" json:"message_id"`
	Topic     string `gorm:"type:varchar(128);not null" json:"topic"`
	// MsgKey 是保序键：同一业务对象的消息共用一个 key，
	// 消费端据此保证串行（现在靠业务状态互斥，多实例时改成按 key 分区）。
	MsgKey string `gorm:"type:varchar(64);not null" json:"msg_key"`

	Payload Payload `gorm:"type:jsonb;not null" json:"payload"`

	Status      Status     `gorm:"not null;default:0" json:"status"`
	Attempts    int        `gorm:"not null;default:0" json:"attempts"`
	AvailableAt time.Time  `gorm:"not null" json:"available_at"`
	LastError   *string    `gorm:"type:text" json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

// TableName 写死表名，避免结构体改名时静默换表。
func (Message) TableName() string { return "outbox" }

// New 构造一条待发送消息。message_id 由数据库生成，
// 这样即使业务代码分多个进程写，也不会撞号。
func New(topic, msgKey string, payload map[string]any) *Message {
	return &Message{
		Topic:       topic,
		MsgKey:      msgKey,
		Payload:     Payload(payload),
		Status:      StatusPending,
		AvailableAt: time.Now().UTC(),
	}
}
