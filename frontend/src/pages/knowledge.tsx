import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  BookOpen,
  ChevronLeft,
  ChevronRight,
  FileText,
  Loader2,
  NotebookPen,
  Upload,
} from 'lucide-react'
import { useRef, useState, type ChangeEvent, type FormEvent } from 'react'

import { documentKeys, createNote, fetchChunks, fetchDocument, fetchDocuments, uploadDocument } from '@/api/documents'
import { describeError } from '@/api/errors'
import type { ChunkView, DocumentSourceType, DocumentStatus, DocumentView } from '@/api/types'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { PageHeader } from '@/components/page-header'
import { StatusPill } from '@/components/status-pill'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'

const PAGE_SIZE = 20
/** 分块接口一次最多返回 100 条（后端上限），页面据此提示"未全部显示"。 */
const CHUNK_PAGE_SIZE = 100

const sourceLabel: Record<DocumentSourceType, string> = {
  file: '文件',
  note: '笔记',
  web: '网页',
}

const statusMeta: Record<DocumentStatus, { label: string; tone: 'ok' | 'warn' | 'danger' | 'muted' }> = {
  pending: { label: '待处理', tone: 'muted' },
  parsing: { label: '解析中', tone: 'warn' },
  indexing: { label: '索引中', tone: 'warn' },
  ready: { label: '已解析', tone: 'ok' },
  failed: { label: '失败', tone: 'danger' },
  cancelling: { label: '取消中', tone: 'muted' },
}

/**
 * 知识库页（Phase 4）：上传文件 / 新建笔记 / 查看分块。
 *
 * 本阶段只做"文本进来、切分、可见"，没有向量与检索：
 * 所以页面明确写"尚未索引"，而不是用一个假的搜索框假装能用。
 */
export function KnowledgePage() {
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [page, setPage] = useState(1)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [composerOpen, setComposerOpen] = useState(false)

  const offset = (page - 1) * PAGE_SIZE
  const listQuery = useQuery({
    queryKey: documentKeys.list(PAGE_SIZE, offset),
    queryFn: ({ signal }) => fetchDocuments(PAGE_SIZE, offset, signal),
  })

  const items = listQuery.data?.items ?? []
  const total = listQuery.data?.total ?? 0
  // 不选时默认看最新一条：新增后列表会刷新，用户不需要再点一下。
  const activeId = selectedId ?? items[0]?.id ?? null

  const detailQuery = useQuery({
    queryKey: documentKeys.detail(activeId ?? 0),
    queryFn: ({ signal }) => fetchDocument(activeId as number, signal),
    enabled: activeId !== null,
  })
  const chunksQuery = useQuery({
    queryKey: documentKeys.chunks(activeId ?? 0),
    queryFn: ({ signal }) => fetchChunks(activeId as number, signal),
    enabled: activeId !== null,
  })

  const afterCreate = (doc: DocumentView) => {
    setSelectedId(doc.id)
    setPage(1)
    setComposerOpen(false)
    // 失效整个 documents 前缀：列表、详情、分块一起重取，
    // 比手工挑几个 key 更不容易漏。
    void queryClient.invalidateQueries({ queryKey: documentKeys.all })
  }

  const uploadMutation = useMutation({ mutationFn: uploadDocument, onSuccess: afterCreate })
  const noteMutation = useMutation({
    mutationFn: (input: { title: string; content: string }) => createNote(input.content, input.title),
    onSuccess: afterCreate,
  })

  const onFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    // 清空 value：否则再选同一个文件不会触发 change，用户会以为点击没生效。
    event.target.value = ''
    if (file) {
      uploadMutation.mutate(file)
    }
  }

  const retryAll = () => {
    void listQuery.refetch()
    void detailQuery.refetch()
    void chunksQuery.refetch()
  }

  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-6">
      <PageHeader
        title="知识库"
        description="导入 Markdown / 纯文本或直接写笔记，系统解析并切分成分块。向量与检索在 Phase 7–8 接入，当前只做切分。"
        actions={
          <>
            <input
              ref={fileInputRef}
              type="file"
              accept=".md,.markdown,.txt,.text"
              className="hidden"
              onChange={onFileChange}
              aria-label="选择要导入的文件"
            />
            <Button
              variant="outline"
              size="sm"
              onClick={() => setComposerOpen((open) => !open)}
              aria-expanded={composerOpen}
            >
              <NotebookPen aria-hidden="true" />
              写笔记
            </Button>
            <Button size="sm" onClick={() => fileInputRef.current?.click()} disabled={uploadMutation.isPending}>
              {uploadMutation.isPending ? (
                <Loader2 className="animate-spin" aria-hidden="true" />
              ) : (
                <Upload aria-hidden="true" />
              )}
              {uploadMutation.isPending ? '导入中' : '上传文件'}
            </Button>
          </>
        }
      />

      {uploadMutation.error ? (
        <InlineError title="文件导入失败" description={describeError(uploadMutation.error)} />
      ) : null}
      {noteMutation.error ? (
        <InlineError title="笔记保存失败" description={describeError(noteMutation.error)} />
      ) : null}

      {composerOpen ? (
        <NoteComposer
          saving={noteMutation.isPending}
          onCancel={() => setComposerOpen(false)}
          onSubmit={(input) => noteMutation.mutate(input)}
        />
      ) : null}

      <div className="grid gap-6 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)]">
        <section aria-label="文档列表" className="space-y-3">
          {listQuery.isPending ? (
            <ListSkeleton />
          ) : listQuery.error ? (
            <ErrorState
              title="读取文档列表失败"
              description={describeError(listQuery.error)}
              onRetry={() => void listQuery.refetch()}
              retrying={listQuery.isFetching}
            />
          ) : items.length === 0 ? (
            <EmptyState
              icon={BookOpen}
              title="还没有文档"
              description="上传一个 .md / .txt 文件，或点右上角「写笔记」新建一条。"
              action={
                <Button variant="outline" size="sm" onClick={() => fileInputRef.current?.click()}>
                  <Upload aria-hidden="true" />
                  上传文件
                </Button>
              }
            />
          ) : (
            <Card className="overflow-hidden py-0">
              <ul className="divide-y">
                {items.map((doc) => (
                  <li key={doc.id}>
                    <button
                      type="button"
                      onClick={() => setSelectedId(doc.id)}
                      aria-current={doc.id === activeId ? 'true' : undefined}
                      className="hover:bg-accent/60 aria-[current=true]:bg-accent w-full space-y-1 px-4 py-3 text-left transition-colors"
                    >
                      <p className="truncate text-sm font-medium">{doc.title}</p>
                      <p className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                        <span>{sourceLabel[doc.source_type]}</span>
                        <span aria-hidden="true">·</span>
                        <span>{doc.chunk_count} 块</span>
                        <span aria-hidden="true">·</span>
                        <span>{formatDate(doc.created_at)}</span>
                      </p>
                    </button>
                  </li>
                ))}
              </ul>
            </Card>
          )}

          {total > PAGE_SIZE ? (
            <div className="flex items-center justify-between gap-2">
              <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
                <ChevronLeft aria-hidden="true" />
                上一页
              </Button>
              <p className="text-muted-foreground text-xs">
                第 {page} / {pageCount} 页 · 共 {total} 条
              </p>
              <Button
                variant="outline"
                size="sm"
                disabled={page >= pageCount}
                onClick={() => setPage((p) => p + 1)}
              >
                下一页
                <ChevronRight aria-hidden="true" />
              </Button>
            </div>
          ) : null}
        </section>

        <section aria-label="文档详情">
          {activeId === null ? (
            <EmptyState
              icon={FileText}
              title="还没有可查看的文档"
              description="左侧选择一篇文档后，这里会显示它的分块与偏移信息。"
            />
          ) : detailQuery.isPending ? (
            <ListSkeleton />
          ) : detailQuery.error ? (
            <ErrorState
              title="读取文档详情失败"
              description={describeError(detailQuery.error)}
              onRetry={retryAll}
              retrying={detailQuery.isFetching}
            />
          ) : (
            <div className="space-y-4">
              <DocumentMeta doc={detailQuery.data} />

              {chunksQuery.isPending ? (
                <ListSkeleton />
              ) : chunksQuery.error ? (
                <ErrorState
                  title="读取分块失败"
                  description={describeError(chunksQuery.error)}
                  onRetry={() => void chunksQuery.refetch()}
                  retrying={chunksQuery.isFetching}
                />
              ) : (
                <ChunkList
                  chunks={chunksQuery.data.items}
                  total={chunksQuery.data.total}
                  maxReturned={CHUNK_PAGE_SIZE}
                />
              )}
            </div>
          )}
        </section>
      </div>
    </div>
  )
}

function DocumentMeta({ doc }: { doc: DocumentView }) {
  const status = statusMeta[doc.status]
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          {doc.title}
          <StatusPill tone={status.tone}>{status.label}</StatusPill>
          {/* 状态 ready 只代表"解析完成"；没有向量时检索用不了，必须说清楚。 */}
          <StatusPill tone="muted">尚未索引</StatusPill>
        </CardTitle>
        <CardDescription>
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <Badge variant="outline">{sourceLabel[doc.source_type]}</Badge>
            <span>{doc.mime}</span>
            <span aria-hidden="true">·</span>
            <span>{formatBytes(doc.size_bytes)}</span>
            <span aria-hidden="true">·</span>
            <span>{doc.chunk_count} 个分块</span>
            <span aria-hidden="true">·</span>
            <span>{formatDate(doc.created_at)}</span>
          </span>
        </CardDescription>
        <CardDescription className="font-mono text-xs break-all">sha256: {doc.content_hash}</CardDescription>
      </CardHeader>
    </Card>
  )
}

function ChunkList({ chunks, total, maxReturned }: { chunks: ChunkView[]; total: number; maxReturned: number }) {
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-sm font-medium">分块预览</h2>
        <p className="text-muted-foreground text-xs">
          偏移以 Unicode 码点计（中文与 emoji 都算 1 个字符），可用于回指原文
        </p>
      </div>

      {chunks.length < total ? (
        <p className="text-muted-foreground text-xs">
          共 {total} 块，这里只显示前 {maxReturned} 块。
        </p>
      ) : null}

      {chunks.map((chunk) => (
        <Card key={chunk.id} className="gap-2">
          <CardHeader className="gap-1">
            <CardTitle className="flex flex-wrap items-center gap-2 text-sm">
              <span className="text-muted-foreground font-mono">#{chunk.seq}</span>
              <span>{chunk.heading || '（文首，无标题）'}</span>
            </CardTitle>
            <CardDescription className="font-mono text-xs">
              [{chunk.start_offset}, {chunk.end_offset}) · 约 {chunk.token_count} tokens
            </CardDescription>
          </CardHeader>
          <CardContent>
            {/* 保留原始换行与 Markdown 标记：分块内容就是原文子串，不做美化，
                因此这里不能用会把空白折叠掉的普通文本渲染。 */}
            <pre className="bg-muted/50 overflow-x-auto rounded-md p-3 font-sans text-sm leading-relaxed whitespace-pre-wrap">
              {chunk.content}
            </pre>
          </CardContent>
        </Card>
      ))}
    </div>
  )
}

function NoteComposer({
  saving,
  onSubmit,
  onCancel,
}: {
  saving: boolean
  onSubmit: (input: { title: string; content: string }) => void
  onCancel: () => void
}) {
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (content.trim() === '') {
      return
    }
    onSubmit({ title: title.trim(), content })
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">新建笔记</CardTitle>
        <CardDescription>正文按 Markdown 解析：# 开头会作为章节标题；标题留空时取正文第一行。</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="space-y-3" onSubmit={submit}>
          <Input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="标题（可选）"
            aria-label="笔记标题"
            maxLength={200}
          />
          <Textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            placeholder={'# 小节标题\n\n正文内容……'}
            aria-label="笔记正文"
            rows={8}
          />
          <div className="flex items-center justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={onCancel} disabled={saving}>
              取消
            </Button>
            <Button type="submit" size="sm" disabled={saving || content.trim() === ''}>
              {saving ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
              保存笔记
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  )
}

function InlineError({ title, description }: { title: string; description: string }) {
  return (
    // role=alert：失败是异步出现的，屏幕阅读器需要主动播报。
    <div role="alert" className="border-destructive/40 bg-destructive/5 rounded-lg border px-3 py-2">
      <p className="text-sm font-medium">{title}</p>
      <p className="text-muted-foreground text-xs break-words">{description}</p>
    </div>
  )
}

function ListSkeleton() {
  return (
    <Card aria-busy="true">
      <CardHeader>
        <CardTitle className="text-base">加载中</CardTitle>
        <CardDescription className="sr-only">正在读取</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
        <Skeleton className="h-4 w-3/5" />
      </CardContent>
    </Card>
  )
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

function formatDate(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) {
    return iso
  }
  return date.toLocaleString('zh-CN', { hour12: false })
}
