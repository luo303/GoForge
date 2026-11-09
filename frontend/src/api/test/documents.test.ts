import { afterEach, describe, expect, it, vi } from 'vitest'

import { createNote, fetchDocument, fetchDocuments } from '@/api/documents'
import type { DocumentView } from '@/api/types'
import { documentFixture, envelope, jsonResponse } from '@/test/fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
})

/**
 * 模拟 Phase 4 的旧后端：文档视图里根本没有 collection / tags 这两个键。
 *
 * 这不是假想状态——本地开发时后端进程没重启就升级过前端，返回的就是这个形状，
 * 当时 `doc.tags.slice()` 直接把整页打成了 React 错误界面。
 */
function legacyDocument(): Partial<DocumentView> {
  const doc: Partial<DocumentView> = { ...documentFixture() }
  delete doc.collection
  delete doc.tags
  return doc
}

describe('文档接口的字段兜底', () => {
  it('列表里缺 collection / tags 时收敛成空串与空数组', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse(envelope({ items: [legacyDocument()], total: 1, limit: 20, offset: 0 })),
      ),
    )

    const page = await fetchDocuments({ collection: '', tag: '', status: '', limit: 20, offset: 0 })

    expect(page.items[0].collection).toBe('')
    expect(page.items[0].tags).toEqual([])
    // 兜底只补这两个字段，其余字段必须原样透传
    expect(page.items[0].chunk_count).toBe(2)
    expect(page.items[0].title).toBe('集成测试笔记')
  })

  it('详情缺 tags 时同样兜底，详情页不会因为 join 而崩', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(envelope(legacyDocument()))))

    const doc = await fetchDocument(2)

    expect(doc.tags).toEqual([])
    expect(doc.collection).toBe('')
  })

  it('导入响应缺 existed 时按新建处理', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(envelope(legacyDocument()))))

    const imported = await createNote({ content: '正文' })

    expect(imported.existed).toBe(false)
    expect(imported.tags).toEqual([])
  })

  it('字段齐全时不做任何改写', async () => {
    const doc = documentFixture({ collection: '工作', tags: ['go', '检索'] })
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(envelope(doc))))

    await expect(fetchDocument(doc.id)).resolves.toEqual(doc)
  })
})
