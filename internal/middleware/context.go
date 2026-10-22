package middleware

import "context"

// 与 request id 相关的 context 键。用未导出的自定义类型避免跨包冲突。
type ctxKeyRequestID struct{}

// WithRequestID 把一个 request id 放进标准库 context.Context，
// 这样不依赖 gin 的 service / repository 层也能拿到它（比如写日志、调用下游）。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID{}, id)
}

// RequestIDFromContext 从 context 中读取 request id（不存在返回空串）。
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKeyRequestID{}).(string); ok {
		return id
	}
	return ""
}
