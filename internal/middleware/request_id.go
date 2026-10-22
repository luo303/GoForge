package middleware

import (
	"crypto/rand"
	"encoding/hex"
)

// RequestIDHeader 是链路追踪用的请求头，沿用业界常见的 X-Request-Id。
const RequestIDHeader = "X-Request-Id"

// newRequestID 生成 16 字节随机数的十六进制表示。
//
// 用 crypto/rand 而不是 math/rand：request id 会进日志并用于关联请求，
// 可预测的随机数在这里没有任何好处。
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 在 Linux 上失败基本意味着系统级问题；
		// 退化为固定值也比 panic 导致整个请求失败更合适。
		return "req-unknown"
	}
	return hex.EncodeToString(b[:])
}
