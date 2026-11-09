import type { DocumentSourceType, DocumentStatus } from '@/api/types'

/** 列表每页条数，与后端的默认值一致；改这里时后端默认值不用动，因为请求里显式带了 limit。 */
export const PAGE_SIZE = 20

/** 分块接口一次最多返回 100 条（后端上限），页面据此提示"未全部显示"。 */
export const CHUNK_PAGE_SIZE = 100

export const sourceLabel: Record<DocumentSourceType, string> = {
  file: '文件',
  note: '笔记',
  web: '网页',
}

type Tone = 'ok' | 'warn' | 'danger' | 'muted'

export const statusMeta: Record<DocumentStatus, { label: string; tone: Tone }> = {
  pending: { label: '待处理', tone: 'muted' },
  parsing: { label: '解析中', tone: 'warn' },
  indexing: { label: '索引中', tone: 'warn' },
  ready: { label: '已解析', tone: 'ok' },
  failed: { label: '失败', tone: 'danger' },
  cancelling: { label: '取消中', tone: 'muted' },
}

/** 状态下拉项。第一项是"不过滤"，所以值是空串而不是 undefined。 */
export const statusOptions: { value: DocumentStatus | ''; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'ready', label: statusMeta.ready.label },
  { value: 'pending', label: statusMeta.pending.label },
  { value: 'parsing', label: statusMeta.parsing.label },
  { value: 'indexing', label: statusMeta.indexing.label },
  { value: 'failed', label: statusMeta.failed.label },
  { value: 'cancelling', label: statusMeta.cancelling.label },
]

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

export function formatDate(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) {
    return iso
  }
  return date.toLocaleString('zh-CN', { hour12: false })
}

/**
 * 把"逗号分隔的输入框内容"拆成标签数组。
 *
 * 去空白、丢空项、按原顺序去重：与服务端 normalizeTags 的规则保持一致。
 * 前端先做一遍不是重复劳动——用户能立刻看到自己输入的会被切成几个标签，
 * 而不用等一次往返才知道。
 */
export function splitTags(raw: string): string[] {
  const seen = new Set<string>()
  const tags: string[] = []
  for (const piece of raw.split(',')) {
    const tag = piece.trim()
    if (tag === '' || seen.has(tag)) {
      continue
    }
    seen.add(tag)
    tags.push(tag)
  }
  return tags
}
