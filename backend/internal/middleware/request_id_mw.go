package middleware

// 本文件是 request id 的 gin 中间件：把 id 写进三个地方，供不同层使用。
// 与 request_id.go 的分工：那个文件只管"怎么生成"，这里只管"怎么塞进请求上下文"。

import (
	"github.com/gin-gonic/gin"

	"github.com/luo303/noema/backend/internal/httpx"
)

// RequestID 为每个请求生成（或透传）request id，写入 gin.Context、标准库 context 与响应头。
//
// 【Go 语法：闭包工厂】
//
//	func RequestID() gin.HandlerFunc {      // 外层：一个"构造函数"
//	    return func(c *gin.Context) { ... } // 内层：真正被 gin 调用的中间件
//	}
//
// 为什么不直接写 func RequestID(c *gin.Context)？因为 gin 要求中间件签名固定为
// func(*gin.Context)，而中间件往往需要外部依赖（日志器、配置、超时时间）。
// 用"外层函数接收依赖 + 返回内层函数"就能把依赖捕获进去 —— 这就是闭包。
// 本函数没有依赖，仍然保持这个形式，是为了和 Logger(log) / Recovery(log, isDev) 统一。
//
// 【和 Node 的关键差异】
// Go 不会自动隐式传递上下文。Node 里 AsyncLocalStorage.run(store, fn) 之后，
// 任意深度的函数都能取到 store；Go 里 request id 必须显式挂在 gin.Context 或
// context.Context 上，沿着函数参数一路传下去。这不是缺陷，是"显式优于隐式"的取舍：
// 代价是签名变长，收益是任何函数拿到 ctx 就一定能看到全部上下文，没有魔法。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 上游（网关 / 另一个服务）已经带了 id 就复用，这样跨服务的链路才能接上。
		// 取不到时（""）才自己生成。
		id := c.GetHeader(RequestIDHeader)
		if id == "" {
			id = newRequestID()
		}

		// 写法一：gin.Context 自己的键值抽屉。gin 内部是 map + 读写锁，并发安全。
		// 供 httpx.RequestID(c)、日志中间件等"手里有 *gin.Context"的代码使用。
		c.Set(httpx.RequestIDKey, id)

		// 写法二：标准库 context。
		// 【关键语法】http.Request 是不可变的（immutable）设计：
		// WithContext 不会修改原请求，而是**复制一份新请求**并返回。
		// 所以必须赋值回 c.Request，否则这个新请求立刻被丢弃，等于什么都没做。
		// 这一步是新手最常漏的：漏了它，service/repository 层拿到的 ctx 里没有 id。
		c.Request = c.Request.WithContext(WithRequestID(c.Request.Context(), id))

		// 写法三：响应头。让调用方能拿到 id 并反馈给客服/运维做排查。
		c.Header(RequestIDHeader, id)

		// c.Next() 执行链上后续的 handler，全部执行完才返回这里。
		// gin 的中间件链用索引推进：Next 相当于"把控制权交下去，等它回来"。
		c.Next()
	}
}
