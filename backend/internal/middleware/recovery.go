package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/httpx"
)

// Recovery 捕获 handler 中的 panic，记录堆栈并返回 500。
//
// 为什么要自己写而不是用 gin.Recovery()？
// gin 默认版本只往日志打一行，且会把 panic 信息直接写入响应。
// 这里改成结构化日志 + 统一响应格式，并且只在开发环境返回具体原因。
//
// 注意：recover 只能捕获"同一个 goroutine"里的 panic。
// 如果你在 handler 里 go func() { ... }() 启动协程，那里的 panic 仍然会让整个进程退出，
// 所以所有后台 goroutine 都必须自己 defer recover（阶段五的消费者会演示）。
func Recovery(log *zap.Logger, isDev bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			log.Error("panic recovered",
				zap.String("request_id", httpx.RequestID(c)),
				zap.String("path", c.Request.URL.Path),
				zap.Any("panic", rec),
				zap.ByteString("stack", debug.Stack()),
			)

			if c.Writer.Written() {
				// 响应已经写出去了，无法再改状态码，只能中断后续 handler。
				c.Abort()
				return
			}

			err := httpx.ErrInternal
			if isDev {
				err = err.WithMessage("panic: %v", rec)
			}
			if rec == http.ErrAbortHandler {
				// 标准库约定的"主动中断"，不当作错误处理。
				c.Abort()
				return
			}
			httpx.Fail(c, err)
		}()
		c.Next()
	}
}
