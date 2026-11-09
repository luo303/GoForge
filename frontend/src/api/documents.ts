import { apiRequest } from '@/api/client'
import type { ChunkList, DocumentList, DocumentView } from '@/api/types'

/**
 * 文档相关的查询键。
 *
 * 列表按分页参数分开缓存：翻回上一页时能直接命中缓存，而不是重新请求。
 * 变更操作统一失效 `all` 前缀，避免"新增成功但列表还是旧的"这类问题。
 */
export const documentKeys = {
  all: ['documents'] as const,
  list: (limit: number, offset: number) => ['documents', 'list', limit, offset] as const,
  detail: (id: number) => ['documents', 'detail', id] as const,
  chunks: (id: number) => ['documents', 'chunks', id] as const,
}

export function fetchDocuments(limit: number, offset: number, signal?: AbortSignal): Promise<DocumentList> {
  return apiRequest<DocumentList>(`/documents?limit=${limit}&offset=${offset}`, { signal })
}

export function fetchDocument(id: number, signal?: AbortSignal): Promise<DocumentView> {
  return apiRequest<DocumentView>(`/documents/${id}`, { signal })
}

export function fetchChunks(id: number, signal?: AbortSignal): Promise<ChunkList> {
  // 一页取 100 个分块：Phase 4 的文档都很短，先不做分块翻页，
  // 超过 100 块时页面会明确提示"只显示前 100 块"。
  return apiRequest<ChunkList>(`/documents/${id}/chunks?limit=100&offset=0`, { signal })
}

export function uploadDocument(file: File): Promise<DocumentView> {
  const body = new FormData()
  body.append('file', file)
  return apiRequest<DocumentView>('/documents', {
    method: 'POST',
    body,
    // 上传比普通请求慢：解析与切分在同一个请求里完成，10s 默认值不够稳。
    timeoutMs: 60_000,
  })
}

export function createNote(content: string, title?: string): Promise<DocumentView> {
  return apiRequest<DocumentView>('/documents/note', {
    method: 'POST',
    body: title ? { title, content } : { content },
  })
}
