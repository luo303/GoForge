package middleware

// 本文件只做一件事：把 request id 放进标准库 context.Context。
// 为什么要单独一层封装，而不让业务代码直接用 context.WithValue(stringKey, id)？
// 见下面 ctxKeyRequestID 的说明。

import "context"

// ctxKeyRequestID 是这个 context 键的类型。
//
// 【Go 语法：为什么用一个"未导出的空结构体类型"当 key】
//
// context.WithValue 的 key 类型是 any，理论上字符串也能当 key。但用字符串有两个坑：
//
//  1. **包与包之间的键冲突**：A 包写 ctx = context.WithValue(ctx, "id", 1)，
//     B 包写 context.WithValue(ctx, "id", "abc")，互相覆盖且编译器不管。
//     用本包私有的类型做 key，别的包根本无法构造出同类型的值（类型不导出），冲突不可能发生。
//
//  2. **分配开销**：struct{} 是零大小类型，&ctxKeyRequestID{} 这类值不占内存，
//     `ctxKeyRequestID{}` 作为值传递时也不分配。比字符串常量更省。
//
// 这种"未导出类型 + 空结构体"是 Go 标准库（net/http、grpc）里通行的做法。
type ctxKeyRequestID struct{}

// WithRequestID 把一个 request id 放进标准库 context.Context，
// 这样不依赖 gin 的 service / repository 层也能拿到它（比如写日志、调用下游）。
//
// 【机制】context 是不可变的：WithValue 返回一个新 context，父子链式嵌套，
// 旧的那个不受影响。这也意味着取值是从内向外逐层查找，层数太深会有微小开销，
// 所以 context 里只该放"请求级元数据"（trace id、用户 id、超时），不要拿来传业务参数。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID{}, id)
}

// RequestIDFromContext 从 context 中读取 request id（不存在返回空串）。
//
// 【Go 语法：类型断言 + comma-ok】
//
//	v, ok := ctx.Value(key).(string)
//
// ctx.Value 返回 any（可能是 nil），必须断言才能当 string 用。
// 写成 ctx.Value(key).(string) 而 ctx 里没有这个键时，会直接 panic：
// 因为 nil 接口断言成 string 必然失败。所以这里一律用带 ok 的形式。
//
// 【易错点】别把这个函数写成 RequestIDFromContext(ctx) string 之后又在调用处
// 假设一定有值。上下文可能来自测试、可能来自一个没挂中间件的路由，空串是合法返回。
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKeyRequestID{}).(string); ok {
		return id
	}
	return ""
}
