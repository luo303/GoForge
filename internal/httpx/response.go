// Package httpx 放 HTTP 层的公共约定：统一响应结构、业务错误类型、错误到 HTTP 的映射。
//
// 之所以单独成包而不是塞进 handler：中间件、handler、甚至 main 都需要写响应，
// 但又不能让它们互相 import —— 这个包没有任何项目内依赖，任何层都可以安全引用。
package httpx

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Envelope 是所有 API 返回的统一结构。
//
//	成功：{"code":"OK","message":"success","data":{...},"request_id":"..."}
//	失败：{"code":"INVALID_ARGUMENT","message":"...","data":null,"request_id":"..."}
//
// 与 NestJS 的全局拦截器 + 异常过滤器等价，只是 Go 里用普通函数显式调用。
type Envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// RequestIDKey 是 request id 在 gin.Context 中的键。
const RequestIDKey = "request_id"

// RequestID 返回当前请求的 request id（由 middleware 写入，可能为空）。
func RequestID(c *gin.Context) string {
	if v, ok := c.Get(RequestIDKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// OK 返回 200 + 数据。
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{Code: "OK", Message: "success", Data: data, RequestID: RequestID(c)})
}

// Created 返回 201 + 数据。
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Envelope{Code: "OK", Message: "success", Data: data, RequestID: RequestID(c)})
}

// Fail 返回错误响应。业务错误会被转换成对应的 HTTP 状态码。
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
func Abort(c *gin.Context, err error) {
	if err == nil {
		c.Abort()
		return
	}
	Fail(c, err)
}
