import { ApiError } from '@/api/errors'

/**
 * 统一 API 客户端。
 *
 * 后端所有接口都返回同一个信封：{ code, message, data, request_id }。
 * 前端只在**这一处**解信封，页面拿到的永远是 data 本身，
 * 这样新增页面时不必重复写 `res.data.data` 这类容易写错的取值。
 */
const API_BASE = import.meta.env.VITE_API_BASE_URL ?? '/api/v1'
const DEFAULT_TIMEOUT_MS = 15_000

type Envelope<T> = {
  code: string
  message: string
  data?: T
  request_id?: string
}

export type RequestOptions = {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  body?: unknown
  signal?: AbortSignal
  timeoutMs?: number
}

export async function apiRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal, timeoutMs = DEFAULT_TIMEOUT_MS } = options

  // 超时不用 AbortSignal.timeout：它在新环境才可用，而这里两行 AbortController
  // 就能做到同样的事，还能和调用方传入的 signal 合并（任一触发即中止）。
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  const abortFromCaller = () => controller.abort()
  signal?.addEventListener('abort', abortFromCaller)

  let response: Response
  try {
    // FormData 必须让浏览器自己带 boundary，手写 Content-Type 会破坏 multipart；
    // JSON 则必须显式声明，否则后端按字符串接收。
    const isFormData = typeof FormData !== 'undefined' && body instanceof FormData
    response = await fetch(`${API_BASE}${path}`, {
      method,
      headers: body === undefined || isFormData ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : isFormData ? body : JSON.stringify(body),
      signal: controller.signal,
    })
  } catch (error) {
    if (signal?.aborted) {
      throw new ApiError('请求已取消', { kind: 'aborted', cause: error })
    }
    if (controller.signal.aborted) {
      throw new ApiError(`请求超时（${timeoutMs / 1000}s）`, { kind: 'timeout', cause: error })
    }
    // fetch 只在网络层失败时 reject，此时拿不到状态码，只能给可执行的提示
    throw new ApiError('无法连接后端服务，确认 go run ./cmd/server 已启动', {
      kind: 'network',
      cause: error,
    })
  } finally {
    clearTimeout(timer)
    signal?.removeEventListener('abort', abortFromCaller)
  }

  const raw = await response.text()
  const payload = parseJson(raw, response.status)
  const envelope = isEnvelope(payload) ? payload : undefined

  if (!response.ok) {
    throw new ApiError(envelope?.message || `服务返回 HTTP ${response.status}`, {
      kind: 'http',
      status: response.status,
      code: envelope?.code,
      requestId: envelope?.request_id,
    })
  }

  if (!envelope) {
    throw new ApiError('响应不符合统一信封格式，可能是前后端版本不一致', {
      kind: 'malformed',
      status: response.status,
    })
  }

  if (envelope.code !== 'OK') {
    throw new ApiError(envelope.message || `业务错误：${envelope.code}`, {
      kind: 'business',
      status: response.status,
      code: envelope.code,
      requestId: envelope.request_id,
    })
  }

  return envelope.data as T
}

function parseJson(raw: string, status: number): unknown {
  if (raw === '') {
    return undefined
  }
  try {
    return JSON.parse(raw) as unknown
  } catch (error) {
    throw new ApiError(`响应不是合法 JSON（HTTP ${status}）`, { kind: 'malformed', status, cause: error })
  }
}

function isEnvelope(value: unknown): value is Envelope<unknown> {
  return (
    typeof value === 'object' &&
    value !== null &&
    'code' in value &&
    typeof (value as { code: unknown }).code === 'string'
  )
}
