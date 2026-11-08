package middleware

// 本文件只负责"生成一个 request id"，不含任何 HTTP 逻辑。
// 拆出来的原因：生成规则和 gin 无关，测试或命令行工具也可能想用同一个规则。

import (
	"crypto/rand"
	"encoding/hex"
)

// RequestIDHeader 是链路追踪用的请求头，沿用业界常见的 X-Request-Id。
//
// 【Go 语法：const 与类型推断】
// 没写类型和 = 的类型，由右侧常量表达式推断（这里是 untyped string，用的时候自动当 string）。
// 多处引用的常量必须定义一次，否则"客户端传 x-request-id 小写、服务端读 X-Request-Id"
// 这种大小写不一致的问题会散落在代码各处。
const RequestIDHeader = "X-Request-Id"

// newRequestID 生成 16 字节随机数的十六进制表示（32 个字符）。
//
// 【Go 语法：数组 vs 切片】
//
//	var b [16]byte    // 数组：长度是类型的一部分，[16]byte 和 [32]byte 是不同类型，值语义（赋值即复制）
//	b[:]              // 切片：对数组的"视图"，类型 []byte，引用语义
//
// 这里需要固定 16 字节，所以用数组；传给 rand.Read 和 hex.EncodeToString 时
// 都要用 b[:] 转成切片，因为这两个函数签名收的是 []byte。
//
// 【为什么用 crypto/rand 而不是 math/rand】
// math/rand 是可预测的伪随机（同一 seed 出同一序列），Go 1.20 起虽自动播种，
// 但对"需要不可猜测"的场景仍不合格。request id 会进日志、用于跨服务关联，
// 可预测意味着别人能伪造、能探测，没有任何好处，所以用系统熵源。
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 【Go 的 error 处理惯用法】
		// 函数返回 (n int, err error)，这里只关心 err，用 _ 丢弃第一个返回值。
		// 注意：_ 不是"忽略错误"，它只是"不绑定变量"，err 仍然被检查了。
		//
		// 【为什么返回固定值而不是 panic】
		// crypto/rand 在 Linux 上失败基本意味着系统级异常（/dev/urandom 不可用）。
		// 为了一个 request id 让整个进程崩溃不划算 —— 请求照常处理，只是失去了链路关联能力。
		// 这是一个明确的降级决策，不是疏忽；降级后的值可被日志检索到（req-unknown）。
		return "req-unknown"
	}
	return hex.EncodeToString(b[:])
}
