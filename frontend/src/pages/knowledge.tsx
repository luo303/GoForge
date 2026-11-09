import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { BookOpen, Loader2, NotebookPen, Upload, X } from 'lucide-react'
import { useRef, useState, type ChangeEvent } from 'react'

import {
  createNote,
  deleteDocument,
  documentKeys,
  fetchChunks,
  fetchDocument,
  fetchDocuments,
  reindexDocument,
  updateDocument,
  uploadDocument,
} from '@/api/documents'
import { describeError } from '@/api/errors'
import type { DocumentFilters, DocumentPatch, DocumentQuery, ImportedDocumentView } from '@/api/types'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'

import { DocumentDetail } from '@/pages/knowledge/document-detail'
import { DocumentList } from '@/pages/knowledge/document-list'
import { FilterBar } from '@/pages/knowledge/filter-bar'
import { PAGE_SIZE } from '@/pages/knowledge/format'
import { NoteComposer, type NoteDraft } from '@/pages/knowledge/note-composer'

const emptyFilters: DocumentFilters = { collection: '', tag: '', status: '' }

/**
 * 知识库页（Phase 5）：导入、筛选、编辑分类标签、删除、重索引、查看分块。
 *
 * 页面本身只做编排：取数、变更、把结果交给下面几个视图组件。
 * 具体渲染在 `pages/knowledge/` 下按职责拆开——单文件超过 500 行后
 * 改动一次要滚动很久，拆分是硬性约定而不是偏好。
 *
 * 仍然没有向量与检索：所以详情卡片明确写"尚未索引"，
 * 而不是用一个假的搜索框假装能用。
 */
export function KnowledgePage() {
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [page, setPage] = useState(1)
  const [filters, setFilters] = useState<DocumentFilters>(emptyFilters)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [composerOpen, setComposerOpen] = useState(false)
  const [importNotice, setImportNotice] = useState<{ existed: boolean; title: string } | null>(null)

  const offset = (page - 1) * PAGE_SIZE
  const query: DocumentQuery = { limit: PAGE_SIZE, offset, ...filters }
  const listQuery = useQuery({
    queryKey: documentKeys.list(query),
    queryFn: ({ signal }) => fetchDocuments(query, signal),
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

  const invalidateDocuments = () => {
    // 失效整个 documents 前缀：列表、详情、分块一起重取，
    // 比手工挑几个 key 更不容易漏。
    void queryClient.invalidateQueries({ queryKey: documentKeys.all })
  }

  const afterImport = (doc: ImportedDocumentView) => {
    setSelectedId(doc.id)
    setPage(1)
    setComposerOpen(false)
    // 幂等命中时说清楚"没有新建"，否则用户会以为又存了一份。
    setImportNotice({ existed: doc.existed, title: doc.title })
    invalidateDocuments()
  }

  const uploadMutation = useMutation({ mutationFn: (file: File) => uploadDocument(file), onSuccess: afterImport })
  const noteMutation = useMutation({
    mutationFn: (draft: NoteDraft) =>
      createNote({ content: draft.content, title: draft.title, collection: draft.collection, tags: draft.tags }),
    onSuccess: afterImport,
  })
  const updateMutation = useMutation({
    mutationFn: (patch: DocumentPatch) => updateDocument(activeId as number, patch),
    onSuccess: () => {
      setEditingId(null)
      invalidateDocuments()
    },
  })
  const deleteMutation = useMutation({
    mutationFn: () => deleteDocument(activeId as number),
    onSuccess: () => {
      // 删掉的是当前选中的那一条，选中态必须清掉：
      // 留着它会立刻触发一次注定 404 的详情请求。
      setSelectedId(null)
      setEditingId(null)
      setImportNotice(null)
      invalidateDocuments()
    },
  })
  const reindexMutation = useMutation({
    mutationFn: () => reindexDocument(activeId as number),
    onSuccess: invalidateDocuments,
  })

  const onFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    // 清空 value：否则再选同一个文件不会触发 change，用户会以为点击没生效。
    event.target.value = ''
    if (file) {
      uploadMutation.mutate(file)
    }
  }

  const changeFilters = (next: DocumentFilters) => {
    setFilters(next)
    setPage(1)
    setSelectedId(null)
  }

  const actionError = updateMutation.error ?? deleteMutation.error ?? reindexMutation.error
  // 有筛选条件时的空结果与"一条文档都没有"是两种状态，文案要分开。
  const isFiltered = filters.collection !== '' || filters.tag !== '' || filters.status !== ''

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
      {actionError ? <InlineError title="操作失败" description={describeError(actionError)} /> : null}
      {importNotice ? <ImportNotice notice={importNotice} onDismiss={() => setImportNotice(null)} /> : null}

      {composerOpen ? (
        <NoteComposer
          saving={noteMutation.isPending}
          onCancel={() => setComposerOpen(false)}
          onSubmit={(draft) => noteMutation.mutate(draft)}
        />
      ) : null}

      <FilterBar filters={filters} onChange={changeFilters} />

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
              title={isFiltered ? '没有符合条件的文档' : '还没有文档'}
              description={
                isFiltered
                  ? '换个分类、标签或状态再试，或者清除筛选查看全部。'
                  : '上传一个 .md / .txt 文件，或点右上角「写笔记」新建一条。'
              }
              action={
                isFiltered ? (
                  <Button variant="outline" size="sm" onClick={() => changeFilters(emptyFilters)}>
                    清除筛选
                  </Button>
                ) : (
                  <Button variant="outline" size="sm" onClick={() => fileInputRef.current?.click()}>
                    <Upload aria-hidden="true" />
                    上传文件
                  </Button>
                )
              }
            />
          ) : (
            <DocumentList
              items={items}
              total={total}
              page={page}
              activeId={activeId}
              onSelect={setSelectedId}
              onPageChange={setPage}
            />
          )}
        </section>

        <section aria-label="文档详情">
          {activeId === null ? (
            <EmptyState
              icon={BookOpen}
              title="还没有可查看的文档"
              description="左侧选择一篇文档后，这里会显示它的分块与偏移信息。"
            />
          ) : detailQuery.isPending ? (
            <ListSkeleton />
          ) : detailQuery.error ? (
            <ErrorState
              title="读取文档详情失败"
              description={describeError(detailQuery.error)}
              onRetry={() => void detailQuery.refetch()}
              retrying={detailQuery.isFetching}
            />
          ) : (
            <DocumentDetail
              // key 换文档时重建组件，把编辑态与表单草稿一起复位——
              // 比在 useEffect 里监听 id 变化再 setState 更直接。
              key={detailQuery.data.id}
              doc={detailQuery.data}
              chunks={chunksQuery.data?.items ?? []}
              chunkTotal={chunksQuery.data?.total ?? 0}
              chunksLoading={chunksQuery.isPending}
              savingMeta={updateMutation.isPending}
              reindexing={reindexMutation.isPending}
              deleting={deleteMutation.isPending}
              editing={editingId === detailQuery.data.id}
              onStartEdit={() => setEditingId(detailQuery.data.id)}
              onCancelEdit={() => setEditingId(null)}
              onSaveMeta={(patch) => updateMutation.mutate(patch)}
              onReindex={() => reindexMutation.mutate()}
              onDelete={() => deleteMutation.mutate()}
            />
          )}
        </section>
      </div>
    </div>
  )
}

function ImportNotice({
  notice,
  onDismiss,
}: {
  notice: { existed: boolean; title: string }
  onDismiss: () => void
}) {
  return (
    // role=status：这是"操作完成"的提示，不是错误，用 status 让读屏器温和播报。
    <div role="status" className="border-border bg-muted/50 flex items-start justify-between gap-3 rounded-lg border px-3 py-2">
      <p className="text-sm">
        {notice.existed
          ? `内容与已有文档一致，已复用《${notice.title}》，没有重复入库。`
          : `已导入《${notice.title}》。`}
      </p>
      <Button variant="ghost" size="sm" onClick={onDismiss} aria-label="关闭提示">
        <X aria-hidden="true" />
      </Button>
    </div>
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
    <div aria-busy="true" className="space-y-3">
      <Skeleton className="h-20 w-full" />
      <Skeleton className="h-20 w-full" />
    </div>
  )
}
