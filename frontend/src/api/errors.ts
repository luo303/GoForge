/**
 * 前端错误模型：把"请求失败"拆成可判断的类别，而不是一个笼统的 Error。
 *
 * 为什么要分 kind：界面上的处置方式不同——网络断（提示起后端）、
 * 业务错误（显示后端给的 message）、响应格式错（提示版本不匹配），
 * 只靠 `error.message` 字符串做匹配既脆弱又不可读。
 */
export type ApiErrorKind =
  /** 请求没到达服务：后端没启动、端口不对、断网 */
  | 'network'
  /** 超时：请求发出去了但没在约定时间内返回 */
  | 'timeout'
  /** 主动取消：组件卸载或用户切换页面，不需要提示 */
  | 'aborted'
  /** HTTP 非 2xx，且响应体是标准信封时能拿到后端错误码 */
  | 'http'
  /** HTTP 2xx 但 code 不是 OK */
  | 'business'
  /** 响应不是约定的 JSON 信封 */
  | 'malformed'

type ApiErrorOptions = {
  kind: ApiErrorKind
  status?: number
  code?: string
  requestId?: string
  cause?: unknown
}

export class ApiError extends Error {
  readonly kind: ApiErrorKind
  readonly status?: number
  readonly code?: string
  readonly requestId?: string

  constructor(message: string, options: ApiErrorOptions) {
    // 第二个参数传 cause 是 ES2022 的标准写法：保留原始异常链，
    // 控制台展开时仍能看到底层 fetch 报错，而 message 保持给用户看的中文。
    super(message, { cause: options.cause })
    this.name = 'ApiError'
    this.kind = options.kind
    this.status = options.status
    this.code = options.code
    this.requestId = options.requestId
  }
}

/**
 * 把任意抛出物收敛成一句可展示的文案。
 *
 * 有 request_id 时追加在末尾：本地单用户场景下，它能把界面上的一次失败
 * 直接对到后端日志的那一行，这是最省事的排障入口。
 */
export function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    return error.requestId ? `${error.message}（request_id: ${error.requestId}）` : error.message
  }
  if (error instanceof Error) {
    return error.message
  }
  return '未知错误'
}
