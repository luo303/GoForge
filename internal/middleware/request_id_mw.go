package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/luo303/GoForge/internal/httpx"
)

// RequestID 为每个请求生成（或透传）request id，写入 gin.Context、标准库 context 与响应头。
//
// 和 Node 里 AsyncLocalStorage 的区别：Go 不会自动"隐式"传递上下文，
// request id 必须显式通过 *gin.Context / context.Context 往下传。
// 这正是 Go 的取向：显式优于隐式。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		c.Set(httpx.RequestIDKey, id)
		c.Request = c.Request.WithContext(WithRequestID(c.Request.Context(), id))
		c.Header(RequestIDHeader, id)
		c.Next()
	}
}
