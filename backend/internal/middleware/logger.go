package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/luo303/mindos/backend/internal/httpx"
)

// Logger 记录每个请求的关键信息：方法、路径、状态码、耗时、request id。
//
// 与 NestJS 的 LoggerMiddleware 类似，但这里有意不记录完整请求体/响应体：
// 那些内容往往包含手机号、密码等敏感信息，需要专门的脱敏策略，
// 放在学习项目里只会增加风险。需要排查时结合 request id 查业务日志即可。
func Logger(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next()

		fields := []zap.Field{
			zap.String("request_id", httpx.RequestID(c)),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.Int("status", c.Writer.Status()),
			// 用 latency_ms 而不是 time.Duration：Duration 在 JSON 里是纳秒整数（272039），
			// 人眼和日志检索都不友好。统一成毫秒浮点数，和大多数网关日志的习惯一致。
			zap.Float64("latency_ms", float64(time.Since(start).Microseconds())/1000),
			zap.Int("size", c.Writer.Size()),
			zap.String("client_ip", c.ClientIP()),
		}
		if query != "" {
			fields = append(fields, zap.String("query", query))
		}
		// gin 会把 handler 里 c.Error(err) 记录的错误收集到 c.Errors。
		if len(c.Errors) > 0 {
			fields = append(fields, zap.Strings("errors", c.Errors.Errors()))
		}

		switch {
		case c.Writer.Status() >= 500:
			log.Error("http request", fields...)
		case c.Writer.Status() >= 400:
			log.Warn("http request", fields...)
		default:
			log.Info("http request", fields...)
		}
	}
}
