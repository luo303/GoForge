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

    await vi.waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([url, init]) => String(url) === '/api/v1/documents' && init?.method === 'POST',
      )
      expect(call).toBeDefined()
      expect(call?.[1]?.body).toBeInstanceOf(FormData)
      expect((call?.[1]?.body as FormData).get('file')).toBeInstanceOf(File)
    })
  })
})
