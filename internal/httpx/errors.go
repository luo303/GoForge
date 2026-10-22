package httpx

import (
	"errors"
	"fmt"
	"net/http"
)

// Error 表示一个"可以返回给客户端"的业务错误。
//
// 与 Node 里 class HttpException extends Error 对应；Go 里用 errors.As 做类型断言。
// Unwrap 让业务错误可以包装底层 err（数据库错误、第三方错误）：
// 日志里能拿到完整原因，而对外只暴露 Message。
type Error struct {
	Code       string // 机器可读错误码，例如 USER_NOT_FOUND
	Message    string // 面向调用方的说明
	HTTPStatus int    // 对应的 HTTP 状态码
	cause      error  // 内部原因，不返回给客户端
}

// Error 让 *Error 实现 error 接口。
func (e *Error) Error() string {
	if e.cause != nil {
		return e.Code + ": " + e.Message + ": " + e.cause.Error()
	}
	return e.Code + ": " + e.Message
}

// Unwrap 支持 errors.Is / errors.As 沿链查找。
func (e *Error) Unwrap() error { return e.cause }

// WithMessage 基于预定义错误派生出带自定义提示的新错误。
func (e *Error) WithMessage(format string, args ...any) *Error {
	cp := *e
	if len(args) == 0 {
		cp.Message = format
	} else {
		cp.Message = fmt.Sprintf(format, args...)
	}
	return &cp
}

// Wrap 在保留错误码与提示的前提下附加上内部原因。
// 返回副本，不会修改包级预定义错误，避免并发下互相污染。
func (e *Error) Wrap(cause error) *Error {
	cp := *e
	cp.cause = cause
	return &cp
}

// 预定义错误。多个 handler / service 复用，保证错误码一致。
var (
	ErrInternal           = &Error{Code: "INTERNAL", Message: "internal server error", HTTPStatus: http.StatusInternalServerError}
	ErrInvalidArgument    = &Error{Code: "INVALID_ARGUMENT", Message: "invalid argument", HTTPStatus: http.StatusBadRequest}
	ErrUnauthorized       = &Error{Code: "UNAUTHORIZED", Message: "unauthorized", HTTPStatus: http.StatusUnauthorized}
	ErrForbidden          = &Error{Code: "FORBIDDEN", Message: "forbidden", HTTPStatus: http.StatusForbidden}
	ErrNotFound           = &Error{Code: "NOT_FOUND", Message: "resource not found", HTTPStatus: http.StatusNotFound}
	ErrConflict           = &Error{Code: "CONFLICT", Message: "resource conflict", HTTPStatus: http.StatusConflict}
	ErrServiceUnavailable = &Error{Code: "SERVICE_UNAVAILABLE", Message: "service unavailable", HTTPStatus: http.StatusServiceUnavailable}
)

// IsCode 判断 err 链上是否有指定错误码的 *Error。
func IsCode(err error, code string) bool {
	var appErr *Error
	return errors.As(err, &appErr) && appErr.Code == code
}
