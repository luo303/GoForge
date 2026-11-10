package handler

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/httpx"
)

// VectorCheckFunc 检查 pgvector 扩展是否可用。
// (false, nil) = 库可连但扩展缺失；(_, err) = 数据库故障。
type VectorCheckFunc func(ctx context.Context) (present bool, err error)

// MigrationInfo 是迁移状态的原始数据，由 cmd/server 装配时注入。
type MigrationInfo struct {
	Current int  // 数据库已应用到的版本；0 = 从未迁移
	Latest  int  // 代码中要求的最新版本（来自 embed 的迁移文件）
	Dirty   bool // 上一次迁移执行到一半中断
}

// MigrationStatusFunc 查询迁移状态。返回 error 表示"查不到"（通常是数据库故障），
// handler 会把这种状态如实标为 unknown，而不是猜一个结果。
type MigrationStatusFunc func(ctx context.Context) (MigrationInfo, error)

// StorageCheckFunc 检查对象存储（MinIO）是否可用。
// (false, nil) = 端点可达但 bucket 不存在；(_, err) = 端点不可达或凭据失效。
type StorageCheckFunc func(ctx context.Context) (bucketExists bool, err error)

// SystemHandler 提供 /system/status 诊断接口。
//
// 与 healthz/readyz 的分工：探针回答"活没活、能不能接流量"（给编排系统看）；
// system/status 回答"哪里坏了、坏成什么样"（给人排查用）。所以它永远返回 200，
// 用字段表达健康与否——非 200 会让 curl 场景下的诊断信息更难读到。
type SystemHandler struct {
	version string
	dbPing  PingFunc
	vector  VectorCheckFunc
	migrate MigrationStatusFunc
	storage StorageCheckFunc
}

// NewSystemHandler 创建诊断 handler。四个检查函数都可独立为 nil（对应能力降级），
// 为 nil 时该项输出 unknown，而不是让接口整体失败。
func NewSystemHandler(
	version string,
	dbPing PingFunc,
	vector VectorCheckFunc,
	migrate MigrationStatusFunc,
	storage StorageCheckFunc,
) *SystemHandler {
	return &SystemHandler{
		version: version,
		dbPing:  dbPing,
		vector:  vector,
		migrate: migrate,
		storage: storage,
	}
}

// DependencyStatus 是一项依赖的健康状态。
type DependencyStatus struct {
	Status string `json:"status"`           // up | down | missing | unknown
	Detail string `json:"detail,omitempty"` // 给人看的补充说明
}

// MigrationStatus 是迁移状态对外视图，State 由 handler 计算，调用方不用自己推。
type MigrationStatus struct {
	State          string `json:"state"` // ok | behind | dirty | not_initialized | unknown
	CurrentVersion int    `json:"current_version"`
	LatestVersion  int    `json:"latest_version"`
	Dirty          bool   `json:"dirty"`
	Detail         string `json:"detail,omitempty"`
}

// SystemStatusResponse 是 /system/status 的响应体。
type SystemStatusResponse struct {
	Version   string           `json:"version"`
	Postgres  DependencyStatus `json:"postgres"`
	PGVector  DependencyStatus `json:"pgvector"`
	Storage   DependencyStatus `json:"storage"`
	Migration MigrationStatus  `json:"migration"`
}

// Status 汇总数据库、pgvector、对象存储、迁移四项诊断信息。
func (h *SystemHandler) Status(c *gin.Context) {
	ctx := c.Request.Context()
	httpx.OK(c, SystemStatusResponse{
		Version:   h.version,
		Postgres:  h.checkPostgres(ctx),
		PGVector:  h.checkVector(ctx),
		Storage:   h.checkStorage(ctx),
		Migration: h.checkMigration(ctx),
	})
}

func (h *SystemHandler) checkPostgres(ctx context.Context) DependencyStatus {
	switch {
	case h.dbPing == nil:
		return DependencyStatus{Status: "unknown", Detail: "check not configured"}
	case h.dbPing(ctx) != nil:
		return DependencyStatus{Status: "down", Detail: "connection failed"}
	default:
		return DependencyStatus{Status: "up"}
	}
}

func (h *SystemHandler) checkVector(ctx context.Context) DependencyStatus {
	if h.vector == nil {
		return DependencyStatus{Status: "unknown", Detail: "check not configured"}
	}
	present, err := h.vector(ctx)
	switch {
	case err != nil:
		return DependencyStatus{Status: "unknown", Detail: "database unreachable"}
	case !present:
		return DependencyStatus{Status: "missing", Detail: "run `go run ./cmd/migrate up` to enable pgvector"}
	default:
		return DependencyStatus{Status: "up"}
	}
}

// checkStorage 检查对象存储。这项在 Phase 4 之前不存在——
// 当时原文落本地磁盘，磁盘几乎不会"连不上"；换成 MinIO 后它成了
// 会独立坏掉的外部依赖，而且坏掉的表现是"上传/重索引 500"，
// 不在这里单独报出来，就只能靠翻日志猜。
func (h *SystemHandler) checkStorage(ctx context.Context) DependencyStatus {
	if h.storage == nil {
		return DependencyStatus{Status: "unknown", Detail: "check not configured"}
	}
	exists, err := h.storage(ctx)
	switch {
	case err != nil:
		return DependencyStatus{Status: "down", Detail: "endpoint unreachable or credentials rejected"}
	case !exists:
		// bucket 缺失不需要手工修：服务启动时会自动建，
		// 但"为什么会缺"值得人看一眼（挂错卷、连到了别的实例）。
		return DependencyStatus{Status: "missing", Detail: "bucket not found; restart to recreate, or check MINIO_BUCKET"}
	default:
		return DependencyStatus{Status: "up"}
	}
}

// checkMigration 把原始数据归纳成对外状态。归纳逻辑放在 handler 而不是
// cmd/server 的装配闭包里：状态语义属于接口契约，应该可以被 handler 测试
// 直接覆盖。
func (h *SystemHandler) checkMigration(ctx context.Context) MigrationStatus {
	if h.migrate == nil {
		return MigrationStatus{State: "unknown", Detail: "check not configured"}
	}
	info, err := h.migrate(ctx)
	if err != nil {
		return MigrationStatus{State: "unknown", Detail: "query failed"}
	}
	st := MigrationStatus{
		State:          "ok",
		CurrentVersion: info.Current,
		LatestVersion:  info.Latest,
		Dirty:          info.Dirty,
	}
	switch {
	case info.Dirty:
		st.State = "dirty"
	case info.Current == 0:
		st.State = "not_initialized"
		st.Detail = "run `go run ./cmd/migrate up`"
	case info.Current < info.Latest:
		st.State = "behind"
		st.Detail = "run `go run ./cmd/migrate up`"
	}
	return st
}
