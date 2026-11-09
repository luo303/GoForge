package httpx

// 本文件定义"业务错误"这一层抽象。建议按顺序读：
//  1. Error 结构体 —— 一个业务错误由哪几部分组成
//  2. Error() 方法 —— 它凭什么能当作 error 使用
//  3. Wrap / WithMessage —— 如何在保留错误码的前提下附加信息
//  4. 预定义错误 + IsCode —— 业务代码怎么消费它们
//
// ============================================================================
// 读这个文件之前，必须先建立两个 Go 的心智模型（后面不再重复解释）
// ============================================================================
//
// 【模型一：Go 没有 class，只有 struct + 方法】
//
//	TS:  class AppError extends Error { constructor(...) { ... } }
//	Go:  type Error struct { ... }            ← 只定义数据
//	     func (e *Error) Error() string {...}  ← 方法写在类型外面
//
// 括号里那段 (e *Error) 叫"接收者"（receiver），相当于 TS 里的 this。
// 写 *Error 表示"由指针调用"，写 Error 表示"值也能调"。这不只是写法差别，
// 它会直接决定一个类型能否满足某个接口，见下面的 Error() 方法。
//
// 【模型二：接口是隐式实现的】
//
//	TS:  class Foo implements Bar { ... }   ← 必须显式声明
//	Go:  什么都不写，方法凑齐了就自动满足接口
//
// 标准库的 error 接口定义只有一行：
//
//	type error interface { Error() string }
//
// 所以下面的 *Error 一个 implements 都没写，却天然是一个 error 值。
// 由此得出一个反直觉的结论：**接口通常由使用方定义，而非实现方声明**。
// 本项目的 handler.PingFunc 就是这个思路。

import (
	"errors"
	"fmt"
	"net/http"
)

// Error 表示一个"可以返回给客户端"的业务错误。
//
// 【Go 语法：字段首字母大小写 = 可见性】
//
//	大写 = 导出（包外可访问，相当于 TS 的 public）
//	小写 = 包内私有（由编译器强制，不是靠命名约定）
//
// cause 故意小写：外部拿不到，只能通过 Unwrap() 间接访问。
// 这是把不变量锁在包里的做法，防止别处直接改写内部原因。
type Error struct {
	Code       string // 机器可读错误码，例如 USER_NOT_FOUND
	Message    string // 面向调用方的说明，会原样返回给客户端
	HTTPStatus int    // 对应的 HTTP 状态码
	cause      error  // 内部原因（数据库错误、第三方错误），不返回给客户端
}

// Error 让 *Error 实现 error 接口。
//
// 【为什么接收者是 *Error 而不是 Error】
// Go 的方法集规则：
//
//	类型 T  的方法集 = 接收者为 T 的方法
//	类型 *T 的方法集 = 接收者为 T 和 *T 的方法
//
// 这里写成 (e *Error)，于是只有 *Error 拥有 Error()，Error（值）没有：
// `var err error = &Error{...}` 合法，而 `var err error = Error{...}` 编译不过。
//
// 【为什么选指针】两个实际原因：
//  1. 不复制结构体。Go 的参数传递和赋值都是值拷贝，4 个字段虽小，但这是普遍习惯。
//  2. 与 Wrap / WithMessage 返回 *Error 保持一致。全项目只留一种形态，
//     省得以后在 errors.As 里猜"这里是值还是指针"——那是最难查的一类 bug。
//
// 【易错点】若把接收者改成值 (e Error)，一旦代码里同时出现 Error{} 字面量和 &Error{}，
// 接口判定就会漏，表现为"明明包装过，却取不到错误码"。
func (e *Error) Error() string {
	// 字符串拼接用 + 就够，编译器会优化，不需要 fmt.Sprintf。
	// 错误信息可能出现在热路径上，少一层格式化开销。
	if e.cause != nil {
		return e.Code + ": " + e.Message + ": " + e.cause.Error()
	}
	return e.Code + ": " + e.Message
}

// Unwrap 支持 errors.Is / errors.As 沿链查找。
//
// 【机制：错误链不是语法特性，而是一个约定】
// Go 没有异常栈，它靠一个约定俗成的方法名把因果串起来：
// 任何类型只要实现了 Unwrap() error，标准库的 errors.Is/As 就会顺着继续往里找。
//
//	ErrNotFound.Wrap(dbErr)  →  errors.Is(err, dbErr) == true
//
// 自己写的包装类型只要提供这个方法，就自动接入标准库错误链，无需注册。
func (e *Error) Unwrap() error { return e.cause }

// WithMessage 基于预定义错误派生出带自定义提示的新错误。
//
// 【Go 语法点】
//   - format string, args ...any：args 是变参（variadic），调用时可传 0 个或多个参数，
//     函数内部它就是一个 []any。any 是 interface{} 的别名（Go 1.18 起官方推荐写法）。
//   - cp := *e：结构体是值类型，*e 解引用再赋值 = 一次逐字段浅拷贝。
//     这是 Go 里复制结构体最常用的写法，不需要任何 clone 工具。
//
// 【为什么要判断 len(args) == 0】
// fmt.Sprintf("50% off") 会把 % 当格式符，输出 "50%!o(MISSING)ff"。
// 无参数时直接赋值，才能安全传递含 % 的字面量。错误信息里带百分比、带 SQL 时必踩。
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
//
// 【必须返回副本 —— 本文件最重要的设计】
// 下面 var 块里的 ErrInternal 是包级变量，全项目共享同一个实例。
// 若这里写 e.cause = cause（直接改自己），两个并发请求会互相覆盖对方的失败原因，
// 这本身就是数据竞争（go test -race 能抓到）。日志里会出现"订单错误套着用户查询错误"
// 这种灵异现象。
//
// 所以请记住：**预定义错误是模板，派生必须复制**。errors_test.go 里有测试守着这条。
//
// 【浅拷贝的边界】cp := *e 只复制字段。cause 是接口值（类型指针 + 数据指针），
// 复制后新旧 Error 指向同一个 cause 对象。这里安全，因为本包从不修改 cause，只替换。
// 若将来 cause 变成可变结构体，就得改深拷贝。
func (e *Error) Wrap(cause error) *Error {
	cp := *e
	cp.cause = cause
	return &cp
}

// 预定义错误。多个 handler / service 复用，保证错误码一致。
//
// 【Go 语法：var 块】var ( a = ...; b = ... ) 是批量声明，等价于写多个 var。
// 每个值都是 &Error{...}，即指向结构体字面量的指针。
//
// 【并发安全吗】安全，因为它们是只读的：所有派生操作（WithMessage / Wrap）都返回副本，
// 没有任何代码写这些字段。一旦有人直接写 ErrNotFound.Message = "xxx"，
// 就会同时污染所有请求 —— 这是 AGENTS.md 里禁止的行为。
var (
	ErrInternal             = &Error{Code: "INTERNAL", Message: "internal server error", HTTPStatus: http.StatusInternalServerError}
	ErrInvalidArgument      = &Error{Code: "INVALID_ARGUMENT", Message: "invalid argument", HTTPStatus: http.StatusBadRequest}
	ErrUnauthorized         = &Error{Code: "UNAUTHORIZED", Message: "unauthorized", HTTPStatus: http.StatusUnauthorized}
	ErrForbidden            = &Error{Code: "FORBIDDEN", Message: "forbidden", HTTPStatus: http.StatusForbidden}
	ErrNotFound             = &Error{Code: "NOT_FOUND", Message: "resource not found", HTTPStatus: http.StatusNotFound}
	ErrConflict             = &Error{Code: "CONFLICT", Message: "resource conflict", HTTPStatus: http.StatusConflict}
	ErrPayloadTooLarge      = &Error{Code: "PAYLOAD_TOO_LARGE", Message: "payload too large", HTTPStatus: http.StatusRequestEntityTooLarge}
	ErrUnsupportedMediaType = &Error{Code: "UNSUPPORTED_MEDIA_TYPE", Message: "unsupported media type", HTTPStatus: http.StatusUnsupportedMediaType}
	ErrServiceUnavailable   = &Error{Code: "SERVICE_UNAVAILABLE", Message: "service unavailable", HTTPStatus: http.StatusServiceUnavailable}
)

// IsCode 判断 err 链上是否有指定错误码的 *Error。
//
// 【为什么 errors.As 的第二个参数是 &appErr】
// appErr 的类型是 *Error，&appErr 就是 **Error（指针的指针）。
// 标准库需要指针的指针才能把结果写回调用方的变量 —— Go 全是值传递，
// 只传 *Error 的话，函数内部改的是副本。
//
// 【为什么不用类型断言 err.(*Error)】
// 错误可能被 fmt.Errorf("...: %w") 包了多层，类型断言只看得到最外层。
// 经验法则：**判断或提取底层错误，永远用 errors.Is / errors.As，不要用类型断言。**
func IsCode(err error, code string) bool {
	var appErr *Error
	return errors.As(err, &appErr) && appErr.Code == code
}
