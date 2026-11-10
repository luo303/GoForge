import { apiRequest } from '@/api/client'
import type {
  ChunkList,
  DocumentList,
  DocumentPatch,
  DocumentQuery,
  DocumentView,
  ImportedDocumentView,
} from '@/api/types'

/**
 * 文档相关的查询键。
 *
 * 列表按"整组筛选条件"分开缓存，而不是只按页码：
 * 同一个 offset 在"全部"和"标签=go"下是两份不同的数据，混用一个键会互相覆盖，
 * 用户切换筛选时会看到上一组条件下的残留结果。
 * 变更操作统一失效 `all` 前缀，避免"改成功但列表还是旧的"这类问题。
 */
export const documentKeys = {
  all: ['documents'] as const,
  list: (query: DocumentQuery) => ['documents', 'list', query] as const,
  detail: (id: number) => ['documents', 'detail', id] as const,
  chunks: (id: number) => ['documents', 'chunks', id] as const,
}

/** 只把非空条件放进 URL：后端把空参数当作"不过滤"，但拼出来一堆 `&tag=` 不好读。 */
function buildListQuery(query: DocumentQuery): string {
  const params = new URLSearchParams({
    limit: String(query.limit),
    offset: String(query.offset),
  })
  if (query.collection !== '') {
    params.set('collection', query.collection)
  }
  if (query.tag !== '') {
    params.set('tag', query.tag)
  }
  if (query.status !== '') {
    params.set('status', query.status)
  }
  return params.toString()
}

/**
 * 接口边界上的字段兜底。
 *
 * 后端按契约总是带 collection / tags，但前端保证不了"对面一定是当前版本"：
 * 开发时后端还跑着旧进程、升级期间新旧混跑，都会让这两个键整个缺失。
 * 缺了就把它们当成"未分类 / 无标签"，页面其余部分照常可用——
 * 否则 `doc.tags.slice(...)` 会抛错，React 直接把整页替换成错误界面。
 *
 * 兜底放在这一层而不是每个组件里，是因为页面只该关心 `DocumentView` 这一种形状。
 */
type WireDocumentView = Omit<DocumentView, 'collection' | 'tags'> & {
  collection?: string | null
  tags?: string[] | null
}

function normalizeDocument(raw: WireDocumentView): DocumentView {
  return { ...raw, collection: raw.collection ?? '', tags: raw.tags ?? [] }
}

/**
 * 导入响应多 existed 与 job_id。
 * 两者都做缺省兜底：旧后端没有 job_id 时按"没有待等任务"处理（0），
 * 页面照常显示，不会因为一个整数缺失把整条响应判成非法。
 */
function normalizeImported(
  raw: WireDocumentView & { existed?: boolean; job_id?: number },
): ImportedDocumentView {
  return { ...normalizeDocument(raw), existed: raw.existed ?? false, job_id: raw.job_id ?? 0 }
}

export async function fetchDocuments(
  query: DocumentQuery,
  signal?: AbortSignal,
): Promise<DocumentList> {
  const page = await apiRequest<Omit<DocumentList, 'items'> & { items: WireDocumentView[] }>(
    `/documents?${buildListQuery(query)}`,
    { signal },
  )
  return { ...page, items: page.items.map(normalizeDocument) }
}

export async function fetchDocument(id: number, signal?: AbortSignal): Promise<DocumentView> {
  return normalizeDocument(await apiRequest<WireDocumentView>(`/documents/${id}`, { signal }))
}

export function fetchChunks(id: number, signal?: AbortSignal): Promise<ChunkList> {
  // 一页取 100 个分块：当前文档都很短，先不做分块翻页，
  // 超过 100 块时页面会明确提示"只显示前 100 块"。
  return apiRequest<ChunkList>(`/documents/${id}/chunks?limit=100&offset=0`, { signal })
}

/**
 * 上传文件。collection / tags 走 multipart 的普通字段：
 * 后端按 `tags=a,b` 逗号分隔解析，前端不需要拼多值字段。
 */
export async function uploadDocument(
  file: File,
  meta: { collection?: string; tags?: string[] } = {},
): Promise<ImportedDocumentView> {
  const body = new FormData()
  body.append('file', file)
  if (meta.collection) {
    body.append('collection', meta.collection)
  }
  if (meta.tags && meta.tags.length > 0) {
    body.append('tags', meta.tags.join(','))
  }
  return normalizeImported(
    await apiRequest<WireDocumentView & { existed?: boolean; job_id?: number }>('/documents', {
      method: 'POST',
      body,
      // 上传比普通请求慢：解析与切分在同一个请求里完成，10s 默认值不够稳。
      timeoutMs: 60_000,
    }),
  )
}

export async function createNote(input: {
  content: string
  title?: string
  collection?: string
  tags?: string[]
}): Promise<ImportedDocumentView> {
  // 空值不发：后端对空标题的处理是"取正文第一行"，传 "" 和传 undefined 效果一样，
  // 但少一个键能让请求体更接近用户真实意图。
  const body: Record<string, unknown> = { content: input.content }
  if (input.title) {
    body.title = input.title
  }
  if (input.collection) {
    body.collection = input.collection
  }
  if (input.tags && input.tags.length > 0) {
    body.tags = input.tags
  }
  return normalizeImported(
    await apiRequest<WireDocumentView & { existed?: boolean; job_id?: number }>('/documents/note', {
      method: 'POST',
      body,
    }),
  )
}

export async function updateDocument(id: number, patch: DocumentPatch): Promise<DocumentView> {
  return normalizeDocument(
    await apiRequest<WireDocumentView>(`/documents/${id}`, { method: 'PATCH', body: patch }),
  )
}

export function deleteDocument(id: number): Promise<{ id: number }> {
  return apiRequest<{ id: number }>(`/documents/${id}`, { method: 'DELETE' })
}

/**
 * 发起重新切分。
 *
 * Phase 6 起是异步的：返回 202 + job_id，文档先置 indexing，分块由 worker 重算。
 * 所以不再给 60s 超时——请求里只剩"建任务"这一步。
 */
export async function reindexDocument(id: number): Promise<ImportedDocumentView> {
  return normalizeImported(
    await apiRequest<WireDocumentView & { existed?: boolean; job_id?: number }>(
      `/documents/${id}/reindex`,
      { method: 'POST' },
    ),
  )
}
