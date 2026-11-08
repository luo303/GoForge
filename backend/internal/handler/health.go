// Package handler 是 HTTP 层的入口：解析请求、调用 service、写统一响应。
//
// 这里的 handler 只做"协议相关"的工作（绑定、校验、状态码），
// 业务规则一律交给 service —— 这样同一套业务逻辑以后可以被消息消费者复用。
package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/luo303/noema/backend/internal/httpx"
	"github.com/luo303/noema/backend/internal/platform"
)

// PingFunc 描述一次依赖健康检查。定义在消费方（handler），
// 这样测试时可以注入假的检查函数，而不用真的连数据库。
type PingFunc func(ctx context.Context) error

// HealthHandler 提供健康检查端点。
type HealthHandler struct {
	version   string
	startedAt time.Time
	dbPing    PingFunc
}

// NewHealthHandler 创建健康检查 handler。
// db 可以为 nil（例如未来某天应用不再依赖数据库时的降级场景）。
func NewHealthHandler(db *gorm.DB, dbPingTimeout time.Duration, version string) *HealthHandler {
	var ping PingFunc
	if db != nil {
		ping = func(ctx context.Context) error {
			return platform.DBPing(ctx, dbPingTimeout, db)
		}
	}
	return &HealthHandler{
		version:   version,
		startedAt: time.Now(),
		dbPing:    ping,
	}
}

// HealthResponse 是健康检查的响应体。
type HealthResponse struct {
	Status    string            `json:"status"` // ok | degraded
	Version   string            `json:"version"`
	UptimeSec int64             `json:"uptime_seconds"`
	Checks    map[string]string `json:"checks"`
}

// Live 是"存活检查"：只说明进程还在，不检查外部依赖。
// 容器编排用它判断是否要重启容器（依赖故障不应该触发重启）。
func (h *HealthHandler) Live(c *gin.Context) {
	httpx.OK(c, HealthResponse{
		Status:    "ok",
		Version:   h.version,
		UptimeSec: int64(time.Since(h.startedAt).Seconds()),
		Checks:    map[string]string{}, // 空 map 而不是 nil，避免序列化成 checks: null
	})
}

// Ready 是"就绪检查"：同时检查关键依赖，依赖不可用时返回 503。
// 容器编排用它判断是否可以接收流量。
func (h *HealthHandler) Ready(c *gin.Context) {
	checks := map[string]string{}
	status := http.StatusOK
	overall := "ok"

	if h.dbPing == nil {
		checks["postgres"] = "disabled"
	} else if err := h.dbPing(c.Request.Context()); err != nil {
		checks["postgres"] = "down"
		status = http.StatusServiceUnavailable
		overall = "degraded"
	} else {
		checks["postgres"] = "up"
	}

	body := httpx.Envelope{
		Code:    overall, // 这里 code 和 HTTP 状态码保持一致语义：ok / degraded
		Message: overall,
		Data: HealthResponse{
			Status:    overall,
			Version:   h.version,
			UptimeSec: int64(time.Since(h.startedAt).Seconds()),
			Checks:    checks,
		},
		RequestID: httpx.RequestID(c),
	}
	c.JSON(status, body)
}
