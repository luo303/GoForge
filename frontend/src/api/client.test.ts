import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiRequest } from '@/api/client'
import { ApiError, describeError } from '@/api/errors'
import { envelope, jsonResponse } from '@/test/fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('apiRequest', () => {
  it('解出统一信封里的 data', async () => {
    const fetchMock = vi.fn(async () => jsonResponse(envelope({ current_version: 1 })))
    vi.stubGlobal('fetch', fetchMock)

    await expect(apiRequest<{ current_version: number }>('/system/status')).resolves.toEqual({
      current_version: 1,
    })
    // 请求路径要带上 /api/v1 前缀，否则 vite 代理与生产托管都对不上
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/system/status', expect.objectContaining({ method: 'GET' }))
  })

  it('网络失败归类为 network 并给出可执行提示', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch')
      }),
    )

    const error = await apiRequest('/settings').catch((err: unknown) => err)

    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).kind).toBe('network')
    expect((error as ApiError).message).toContain('无法连接后端服务')
  })

  it('HTTP 非 2xx 时保留状态码、错误码与 request_id', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse({ code: 'NOT_FOUND', message: 'route GET /nope not found', request_id: 'req-42' }, 404),
      ),
    )

    const error = (await apiRequest('/nope').catch((err: unknown) => err)) as ApiError

    expect(error.kind).toBe('http')
    expect(error.status).toBe(404)
    expect(error.code).toBe('NOT_FOUND')
    expect(error.requestId).toBe('req-42')
    expect(describeError(error)).toBe('route GET /nope not found（request_id: req-42）')
  })

  it('HTTP 200 但 code 不是 OK 时按业务错误处理', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse({ code: 'INTERNAL', message: 'internal server error' })),
    )

    const error = (await apiRequest('/system/status').catch((err: unknown) => err)) as ApiError

    expect(error.kind).toBe('business')
    expect(error.code).toBe('INTERNAL')
    expect(error.message).toBe('internal server error')
  })

  it('响应不是 JSON 时不当作成功', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>oops</html>', { status: 200 })))

    const error = (await apiRequest('/system/status').catch((err: unknown) => err)) as ApiError

    expect(error.kind).toBe('malformed')
    expect(error.message).toContain('不是合法 JSON')
  })

  it('响应是 JSON 但没有信封字段时不当作成功', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({ postgres: 'up' })))

    const error = (await apiRequest('/system/status').catch((err: unknown) => err)) as ApiError

    expect(error.kind).toBe('malformed')
  })

  it('超过超时时间归类为 timeout', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (_input: string, init?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener('abort', () => reject(new Error('aborted')))
          }),
      ),
    )

    const error = (await apiRequest('/slow', { timeoutMs: 20 }).catch((err: unknown) => err)) as ApiError

    expect(error.kind).toBe('timeout')
    expect(error.message).toContain('请求超时')
  })

  it('调用方取消不算失败', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (_input: string, init?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener('abort', () => reject(new Error('aborted')))
          }),
      ),
    )

    const controller = new AbortController()
    const promise = apiRequest('/slow', { signal: controller.signal, timeoutMs: 5000 })
    controller.abort()

    await expect(promise).rejects.toMatchObject({ kind: 'aborted' })
  })
})

describe('describeError', () => {
  it('非 ApiError 也能给出可读文案', () => {
    expect(describeError(new Error('boom'))).toBe('boom')
    expect(describeError('plain string')).toBe('未知错误')
  })
})
