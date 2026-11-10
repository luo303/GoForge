import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { DocumentView } from '@/api/types'
import {
  chunkFixture,
  documentFixture,
  documentListFixture,
  envelope,
  jsonResponse,
} from '@/test/fixtures'
import { renderApp } from '@/test/render-app'

/**
 * 知识页测试：覆盖"列表 / 详情 / 分块"三条读取路径与两个写入路径。
 *
 * fetch 用统一的假实现按 URL 分发，而不是逐用例 mock 每个模块：
 * 页面用的是真实 api 客户端与真实 Query，这样信封解析、错误分类、重试
 * 这些行为也在测试范围内——它们正是最容易出问题的地方。
 */
const fetchMock = vi.fn()

beforeEach(() => {
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  fetchMock.mockReset()
})

function jsonOk(body: unknown, status = 200) {
  return Promise.resolve(jsonResponse(body, status))
}

function chunkList(chunks: ReturnType<typeof chunkFixture>[]) {
  return { items: chunks, total: chunks.length, limit: 100, offset: 0 }
}

/** 一个文档 + 一个分块的常规成功路径。 */
function mockHappyPath(doc: DocumentView, content: string) {
  fetchMock.mockImplementation((input: RequestInfo | URL) => {
    const url = String(input)
    if (url === '/api/v1/documents?limit=20&offset=0') {
      return jsonOk(envelope(documentListFixture([doc])))
    }
    if (url === `/api/v1/documents/${doc.id}`) {
      return jsonOk(envelope(doc))
    }
    if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
      return jsonOk(envelope(chunkList([chunkFixture({ content })])))
    }
    return jsonOk({ code: 'NOT_FOUND', message: 'unexpected url: ' + url }, 404)
  })
}

describe('知识库页面', () => {
  it('显示文档列表与分块偏移', async () => {
    const doc = documentFixture({ chunk_count: 1 })
    mockHappyPath(doc, '# 标题\n\n第一段正文。')

    renderApp('/knowledge')

    expect(await screen.findByText('集成测试笔记')).toBeInTheDocument()
    // 分块内容原样渲染：保留 Markdown 标记与换行，不做美化。
    expect(await screen.findByText(/# 标题[\s\S]*第一段正文。/)).toBeInTheDocument()
    expect(screen.getByText('尚未索引')).toBeInTheDocument()
    expect(screen.getByText(/\[0, 12\)/)).toBeInTheDocument()
  })

  it('空列表显示空态而不是空白面板', async () => {
    fetchMock.mockImplementation(() => jsonOk(envelope(documentListFixture([]))))

    renderApp('/knowledge')

    expect(await screen.findByText('还没有文档')).toBeInTheDocument()
    expect(screen.getByText('还没有可查看的文档')).toBeInTheDocument()
    // 空态里的操作按钮与页头按钮同名，这里确认至少有一个可点的上传入口。
    expect(screen.getAllByRole('button', { name: /上传文件/ }).length).toBeGreaterThan(0)
  })

  it('列表请求失败时给出原因与重试', async () => {
    fetchMock.mockImplementation(() =>
      jsonOk({ code: 'INTERNAL', message: 'database unreachable', request_id: 'req-500' }, 500),
    )

    renderApp('/knowledge')

    expect(await screen.findByText('读取文档列表失败')).toBeInTheDocument()
    expect(screen.getByText(/database unreachable（request_id: req-500）/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument()
  })

  it('新建笔记提交正文并在成功后刷新列表', async () => {
    const user = userEvent.setup()
    const created = documentFixture({ id: 9, title: '临时会议记录' })
    let listCalls = 0

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/v1/documents?limit=20&offset=0') {
        listCalls += 1
        return jsonOk(envelope(documentListFixture(listCalls === 1 ? [] : [created])))
      }
      if (url === '/api/v1/documents/note') {
        return jsonOk(envelope(created), 201)
      }
      if (url === '/api/v1/documents/9') {
        return jsonOk(envelope(created))
      }
      if (url === '/api/v1/documents/9/chunks?limit=100&offset=0') {
        return jsonOk(envelope(chunkList([chunkFixture({ content: '# 临时会议记录\n\n正文。' })])))
      }
      throw new Error(`unexpected url: ${url} ${init?.method ?? 'GET'}`)
    })

    renderApp('/knowledge')
    await screen.findByText('还没有文档')

    await user.click(screen.getByRole('button', { name: '写笔记' }))
    await user.type(screen.getByLabelText('笔记标题'), '临时会议记录')
    await user.type(screen.getByLabelText('笔记正文'), '# 临时会议记录\n\n正文。')
    await user.click(screen.getByRole('button', { name: '保存笔记' }))

    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/v1/documents/note',
        expect.objectContaining({ method: 'POST' }),
      )
    })
    const noteCall = fetchMock.mock.calls.find(([url]) => String(url) === '/api/v1/documents/note')
    expect(noteCall).toBeDefined()
    expect(JSON.parse(String(noteCall?.[1]?.body))).toEqual({
      title: '临时会议记录',
      content: '# 临时会议记录\n\n正文。',
    })
    // 成功后列表失效并重新请求，新文档出现在列表里。
    // 用 region 限定范围：详情卡片里也有同样的标题，全局查找会命中两个。
    const list = screen.getByRole('region', { name: '文档列表' })
    expect(await within(list).findByText('临时会议记录')).toBeInTheDocument()
  })

  it('上传文件走 multipart，大小写扩展名交给后端判断', async () => {
    const user = userEvent.setup()
    mockHappyPath(documentFixture(), '正文')

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    const file = new File(['# 笔记\n\n内容'], 'note.md', { type: 'text/markdown' })
    await user.upload(screen.getByLabelText('选择要导入的文件'), file)
    // 选完文件先落到确认面板，不立刻上传。
    expect(await screen.findByText('note.md')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '开始导入' }))

    await vi.waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([url, init]) => String(url) === '/api/v1/documents' && init?.method === 'POST',
      )
      expect(call).toBeDefined()
      expect(call?.[1]?.body).toBeInstanceOf(FormData)
      expect((call?.[1]?.body as FormData).get('file')).toBeInstanceOf(File)
      // 没填分类标签时不该凭空多出这两个字段。
      expect((call?.[1]?.body as FormData).get('collection')).toBeNull()
      expect((call?.[1]?.body as FormData).get('tags')).toBeNull()
    })
  })

  it('上传前可以填分类与标签，随 multipart 一起提交', async () => {
    const user = userEvent.setup()
    mockHappyPath(documentFixture(), '正文')

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    const file = new File(['# 笔记\n\n内容'], 'note.md', { type: 'text/markdown' })
    await user.upload(screen.getByLabelText('选择要导入的文件'), file)
    await user.type(await screen.findByLabelText('文件分类'), '工作')
    await user.type(screen.getByLabelText('文件标签'), 'go, 检索')
    // 标签按逗号切分并去空白，与服务端规则一致。
    expect(screen.getByText('将保存 2 个标签：go、检索')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '开始导入' }))

    await vi.waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([url, init]) => String(url) === '/api/v1/documents' && init?.method === 'POST',
      )
      expect(call).toBeDefined()
      const body = call?.[1]?.body as FormData
      expect(body.get('collection')).toBe('工作')
      expect(body.get('tags')).toBe('go,检索')
    })
  })

  it('取消后关闭面板且不发导入请求', async () => {
    const user = userEvent.setup()
    mockHappyPath(documentFixture(), '正文')

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    const file = new File(['# 笔记'], 'drop.md', { type: 'text/markdown' })
    await user.upload(screen.getByLabelText('选择要导入的文件'), file)
    await user.click(await screen.findByRole('button', { name: '取消' }))

    expect(screen.queryByText('drop.md')).not.toBeInTheDocument()
    const posted = fetchMock.mock.calls.some(
      ([url, init]) => String(url) === '/api/v1/documents' && init?.method === 'POST',
    )
    expect(posted).toBe(false)
  })

  it('标签超出上限时给出提示并挡住提交', async () => {
    const user = userEvent.setup()
    mockHappyPath(documentFixture(), '正文')

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    const file = new File(['# 笔记\n\n内容'], 'note.md', { type: 'text/markdown' })
    await user.upload(screen.getByLabelText('选择要导入的文件'), file)
    // 21 个标签：后端会拒，前端先挡住，不用等一次往返。
    const many = Array.from({ length: 21 }, (_, i) => `t${i}`).join(',')
    await user.type(await screen.findByLabelText('文件标签'), many)

    expect(screen.getByText('最多 20 个标签，当前 21 个')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '开始导入' })).toBeDisabled()
  })
})

/**
 * Phase 5 的文档管理能力：幂等提示、分类标签、筛选、编辑、重索引、删除。
 *
 * 这组用例同样走真实 api 客户端与真实 Query，只把 fetch 换掉：
 * 要验证的正是"点一下之后到底发了什么请求、列表有没有跟着变"。
 */
describe('知识库页面的文档管理', () => {
  it('列表与详情展示分类和标签', async () => {
    const doc = documentFixture({ collection: '工作', tags: ['go', '检索'] })
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([doc])))
      }
      if (url === `/api/v1/documents/${doc.id}`) {
        return jsonOk(envelope(doc))
      }
      if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')

    await screen.findByText('集成测试笔记')
    // 先等详情卡片出现（哈希那行只在详情里），再断言"列表 + 详情各一份"。
    await screen.findByText(/^sha256:/)
    expect(screen.getAllByText('工作').length).toBe(2)
    expect(screen.getAllByText('go').length).toBe(2)
    expect(screen.getAllByText('检索').length).toBe(2)
  })

  it('按标签筛选时把条件带进查询参数', async () => {
    const user = userEvent.setup()
    const tagged = documentFixture({ id: 5, title: '带 go 标签', tags: ['go'] })
    const requested: string[] = []
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      requested.push(url)
      if (url === '/api/v1/documents?limit=20&offset=0' || url === '/api/v1/documents?limit=20&offset=0&tag=go') {
        return jsonOk(envelope(documentListFixture([tagged])))
      }
      if (url === `/api/v1/documents/${tagged.id}`) {
        return jsonOk(envelope(tagged))
      }
      if (url === `/api/v1/documents/${tagged.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('带 go 标签')

    await user.type(screen.getByLabelText('按标签筛选'), 'go')
    await user.click(screen.getByRole('button', { name: '筛选' }))

    await vi.waitFor(() => {
      expect(requested).toContain('/api/v1/documents?limit=20&offset=0&tag=go')
    })
  })

  it('编辑标签只提交改动过的字段', async () => {
    const user = userEvent.setup()
    const doc = documentFixture({ tags: ['go'] })
    const updated = { ...doc, tags: ['go', '检索'] }

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (init?.method === 'PATCH') {
        return jsonOk(envelope(updated))
      }
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([doc])))
      }
      if (url === `/api/v1/documents/${doc.id}`) {
        return jsonOk(envelope(doc))
      }
      if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    await user.click(await screen.findByRole('button', { name: '编辑分类与标签' }))
    const tagInput = await screen.findByLabelText('文档标签')
    await user.clear(tagInput)
    await user.type(tagInput, 'go, 检索')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await vi.waitFor(() => {
      const call = fetchMock.mock.calls.find(([, init]) => init?.method === 'PATCH')
      expect(call).toBeDefined()
      // 标题与分类没动，就不该出现在请求体里。
      expect(JSON.parse(String(call?.[1]?.body))).toEqual({ tags: ['go', '检索'] })
    })
  })

  it('重新切分调用 reindex 接口', async () => {
    const user = userEvent.setup()
    const doc = documentFixture()

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === `/api/v1/documents/${doc.id}/reindex` && init?.method === 'POST') {
        return jsonOk(envelope({ ...doc, chunk_count: 3 }))
      }
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([doc])))
      }
      if (url === `/api/v1/documents/${doc.id}`) {
        return jsonOk(envelope(doc))
      }
      if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    await user.click(await screen.findByRole('button', { name: '重新切分' }))

    await vi.waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([url, init]) => String(url) === `/api/v1/documents/${doc.id}/reindex` && init?.method === 'POST',
      )
      expect(call).toBeDefined()
    })
  })

  it('删除需要二次确认，确认后发出 DELETE 并清空选中', async () => {
    const user = userEvent.setup()
    const doc = documentFixture()
    let deleted = false

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === `/api/v1/documents/${doc.id}` && init?.method === 'DELETE') {
        deleted = true
        return jsonOk(envelope({ id: doc.id }))
      }
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture(deleted ? [] : [doc])))
      }
      if (url === `/api/v1/documents/${doc.id}`) {
        return jsonOk(envelope(doc))
      }
      if (url === `/api/v1/documents/${doc.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('集成测试笔记')

    await user.click(await screen.findByRole('button', { name: '删除' }))
    // 第一次点击只是展开确认区，还不该发请求。
    expect(screen.getByRole('alertdialog', { name: '确认删除文档' })).toBeInTheDocument()
    expect(deleted).toBe(false)

    await user.click(screen.getByRole('button', { name: '确认删除' }))

    await vi.waitFor(() => {
      expect(deleted).toBe(true)
    })
    // 删掉当前文档后不能继续显示它——否则会立刻触发一次注定 404 的详情请求。
    expect(await screen.findByText('还没有可查看的文档')).toBeInTheDocument()
  })

  it('上传命中既有文档时提示没有重复入库', async () => {
    const user = userEvent.setup()
    const existing = documentFixture({ id: 3, title: '已有内容' })

    fetchMock.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/v1/documents' && init?.method === 'POST') {
        return jsonOk(envelope({ ...existing, existed: true }))
      }
      if (url === '/api/v1/documents?limit=20&offset=0') {
        return jsonOk(envelope(documentListFixture([existing])))
      }
      if (url === `/api/v1/documents/${existing.id}`) {
        return jsonOk(envelope(existing))
      }
      if (url === `/api/v1/documents/${existing.id}/chunks?limit=100&offset=0`) {
        return jsonOk(envelope(chunkList([chunkFixture()])))
      }
      return jsonOk({ code: 'NOT_FOUND', message: url }, 404)
    })

    renderApp('/knowledge')
    await screen.findByText('已有内容')

    const file = new File(['# 标题'], 'copy.md', { type: 'text/markdown' })
    await user.upload(screen.getByLabelText('选择要导入的文件'), file)
    await user.click(await screen.findByRole('button', { name: '开始导入' }))

    expect(await screen.findByText(/已复用《已有内容》，没有重复入库/)).toBeInTheDocument()
  })
})
