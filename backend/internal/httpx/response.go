// Package httpx 放 HTTP 层的公共约定：统一响应结构、业务错误类型、错误到 HTTP 的映射。
//
// 之所以单独成包而不是塞进 handler：中间件、handler、甚至 main 都需要写响应，
// 但又不能让它们互相 import —— 这个包不引用项目内任何代码，任何层都能安全引用它。
//
// 包内文件分工：
//
//	errors.go   → 业务错误类型 *Error 与预定义错误码
//	response.go → 统一响应信封 Envelope 与 OK / Fail 等写出函数（本文件）
package httpx

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Envelope 是所有 API 返回的统一结构。
//
//	成功：{"code":"OK","message":"success","data":{...},"request_id":"..."}
//	失败：{"code":"NOT_FOUND","message":"...","request_id":"..."}
//
// 与 NestJS 的全局拦截器 + 异常过滤器等价，只是 Go 里用普通函数显式调用，
// 不依赖框架的 AOP 机制。
//
// 【Go 语法：struct tag】
// 字段后面反引号里那串 `json:"code"` 是标签（tag），本质是贴在字段上的字符串，
// 由 encoding/json 通过**反射**读取，决定序列化后的键名。它不是类型系统的一部分，
// 写错了编译器不报错，运行时静默用字段名（首字母大写变成 "Code"）。
//
// 【omitempty 的真实行为 —— 这里有个容易误解的点】
// omitempty 会在字段为"零值"时省略整个键：
//   - 字符串 ""、数字 0、指针 nil、接口 nil、空切片/空 map 都会被省略
//   - 注意是省略**键**，不是输出 null
//
// 所以失败响应里其实没有 "data" 字段（因为 Data 是 nil 接口），
// 而 README 的示例写的是 "data": null —— 文档与实际有一处小偏差，以代码为准。
// 若确实需要输出 null，应去掉 omitempty。
type Envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// RequestIDKey 是 request id 在 gin.Context 中的键。
const RequestIDKey = "request_id"

// RequestID 返回当前请求的 request id（由 middleware 写入，可能为空）。
//
// 【Go 语法：comma-ok 类型断言】
//
//	v, ok := c.Get(key)   // c.Get 返回 (any, bool)：key 不存在时 ok=false，v=nil
//	s, ok := v.(string)   // 断言 v 的底层类型是 string；失败时 ok=false，s 是零值 ""
//
// 这是 Go 里唯一安全的取值断言方式。直接写 v.(string) 在类型不符时会 panic，
// 只在"逻辑上不可能失败"的地方才这么写。gin 的 c.Get 存的是 any，
// 所以这里必须两层判断：先看 key 在不在，再看类型对不对。
func RequestID(c *gin.Context) string {
	if v, ok := c.Get(RequestIDKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// OK 返回 200 + 数据。
//
// 【Go 语法】参数类型是 any（= interface{}），意味着可以传任意类型，
// c.JSON 内部会做 JSON 序列化。代价是失去了编译期类型检查 ——
// 传一个无法序列化的值（如 chan、func）只会在运行时变成空对象或报错。
// 这是"通用响应函数"的固有取舍，换取的是所有接口共用一套信封格式。
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{Code: "OK", Message: "success", Data: data, RequestID: RequestID(c)})
}

// Created 返回 201 + 数据。
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Envelope{Code: "OK", Message: "success", Data: data, RequestID: RequestID(c)})
}

// Fail 返回错误响应。业务错误会被转换成对应的 HTTP 状态码。
//
// 【流程】
//  1. 用 errors.As 判断 err 链上是否含 *Error，取第一个匹配的；
//  2. 取不到说明是"未分类的意外错误"（比如数据库连接失败），
//     统一兜底成 500，并把原始 err 塞进 cause —— 对外只说 internal server error；
//  3. 用 AbortWithStatusJSON 写响应，它会同时把 gin 的 handler 链标记为"已中止"，
//     后续中间件不会再继续处理。
//
// 【安全要点】appErr.Message 会原样返回给客户端，所以预定义错误的 Message 必须是人话；
// 真正的内部原因只存在于 cause 里，靠日志记录，绝不进响应体。
func Fail(c *gin.Context, err error) {
	var appErr *Error
	if !errors.As(err, &appErr) {
		appErr = ErrInternal.Wrap(err)
	}
	c.AbortWithStatusJSON(appErr.HTTPStatus, Envelope{
		Code:      appErr.Code,
		Message:   appErr.Message,
		RequestID: RequestID(c),
	})
}

// Abort 用于中间件中直接以业务错误中断请求。
//
// 【注意 c.Abort() 和 AbortWithStatusJSON 的区别】
//   - c.Abort() 只是把 gin 内部的 handler 索引置为最大，**不写任何响应**；
//   - AbortWithStatusJSON 才是既中止、又写响应。
//
// 所以 err == nil 这个分支只中断不写响应，用于"不打算回任何内容但必须停下"的场景。
func Abort(c *gin.Context, err error) {
	if err == nil {
		c.Abort()
		return
	}
	Fail(c, err)
}
