// Package router 负责装配 gin 引擎：404/405 统一处理 + 业务路由分组。
//
// 它不认识任何 service 实现，只接收已经构造好的 handler，
// 这样"依赖从哪里来"完全由 cmd/server 决定，router 只关心"路由怎么挂"。
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luo303/mindos/backend/internal/handler"
	"github.com/luo303/mindos/backend/internal/httpx"
	"github.com/luo303/mindos/backend/internal/knowledge"
)

// Deps 收集构建路由需要的依赖。
// 用结构体而不是一长串参数：以后新增 handler 时调用方不必修改函数签名。
type Deps struct {
	Health   *handler.HealthHandler
	System   *handler.SystemHandler
	Settings *handler.SettingsHandler

	// Knowledge 是知识模块（文档入库与分块）。
	Knowledge *knowledge.Handler

	// 后续阶段在这里扩展：
	//   Chat *chat.Handler（Phase 13 起）
}

// Register 把路由注册到已经挂好全局中间件的 engine 上。
// 中间件在 cmd/server 中统一 Use，这里只关心路由表。
func Register(e *gin.Engine, deps Deps) {
	// 健康检查放在根路径，容器和负载均衡可以直接访问。
	e.GET("/healthz", deps.Health.Live)
	e.GET("/readyz", deps.Health.Ready)

	// 404：路由不存在时也返回统一格式，而不是 gin 默认的纯文本。
	e.NoRoute(func(c *gin.Context) {
		httpx.Abort(c, httpx.ErrNotFound.WithMessage(
			"route %s %s not found", c.Request.Method, c.Request.URL.Path,
		))
	})

	// 405：路径存在但方法不对。需要显式打开 HandleMethodNotAllowed。
	e.HandleMethodNotAllowed = true
	e.NoMethod(func(c *gin.Context) {
		httpx.Abort(c, &httpx.Error{
			Code:       "METHOD_NOT_ALLOWED",
			Message:    "method " + c.Request.Method + " not allowed",
			HTTPStatus: http.StatusMethodNotAllowed,
		})
	})

	// 业务路由统一挂在 /api/v1 下，方便以后做版本演进。
	v1 := e.Group("/api/v1")
	v1.GET("/system/status", deps.System.Status)
	v1.GET("/settings", deps.Settings.Get)

	// 知识：导入与重索引都是异步受理，返回 202 + message_id（Phase 6）；
	// 同内容重复导入返回 200 + existed=true（Phase 5）。
	v1.POST("/documents", deps.Knowledge.CreateFile)
	v1.POST("/documents/note", deps.Knowledge.CreateNote)
	v1.GET("/documents", deps.Knowledge.List)
	v1.GET("/documents/:id", deps.Knowledge.Get)
	v1.GET("/documents/:id/chunks", deps.Knowledge.ListChunks)
	v1.PATCH("/documents/:id", deps.Knowledge.Update)
	v1.DELETE("/documents/:id", deps.Knowledge.Delete)
	v1.POST("/documents/:id/reindex", deps.Knowledge.Reindex)
	v1.POST("/documents/:id/cancel", deps.Knowledge.Cancel)
}
