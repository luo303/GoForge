import { Loader2, Pencil, RefreshCw, Trash2 } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import type { ChunkView, DocumentPatch, DocumentView } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill } from '@/components/status-pill'

import { CHUNK_PAGE_SIZE, formatBytes, formatDate, sourceLabel, splitTags, statusMeta } from './format'

type DetailProps = {
  doc: DocumentView
  chunks: ChunkView[]
  chunkTotal: number
  chunksLoading: boolean
  /** 编辑态由页面持有：保存成功后页面才需要把它关掉，组件自己关不了。 */
  editing: boolean
  savingMeta: boolean
  reindexing: boolean
  deleting: boolean
  onStartEdit: () => void
  onCancelEdit: () => void
  onSaveMeta: (patch: DocumentPatch) => void
  onReindex: () => void
  onDelete: () => void
}

/**
 * 文档详情：元信息、编辑表单、危险操作与分块预览。
 *
 * 表单字段的初值来自 doc，页面用 `key={doc.id}` 渲染本组件，
 * 换一条文档时整棵子树重建，草稿自然复位——不需要用 effect 去同步 props。
 */
export function DocumentDetail({
  doc,
  chunks,
  chunkTotal,
  chunksLoading,
  editing,
  savingMeta,
  reindexing,
  deleting,
  onStartEdit,
  onCancelEdit,
  onSaveMeta,
  onReindex,
  onDelete,
}: DetailProps) {
  const [confirmingDelete, setConfirmingDelete] = useState(false)

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="flex flex-wrap items-center gap-2 text-base">
            {doc.title}
            <StatusPill tone={statusMeta[doc.status].tone}>{statusMeta[doc.status].label}</StatusPill>
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
              <span>更新于 {formatDate(doc.updated_at)}</span>
            </span>
          </CardDescription>
          <CardDescription className="font-mono text-xs break-all">sha256: {doc.content_hash}</CardDescription>
        </CardHeader>

        <CardContent className="space-y-3">
          <MetadataRow doc={doc} />

          {editing ? (
            <MetadataForm
              doc={doc}
              saving={savingMeta}
              onCancel={onCancelEdit}
              onSubmit={onSaveMeta}
            />
          ) : (
            <div className="flex flex-wrap items-center gap-2">
              <Button variant="outline" size="sm" onClick={onStartEdit}>
                <Pencil aria-hidden="true" />
                编辑分类与标签
              </Button>
              <Button variant="outline" size="sm" onClick={onReindex} disabled={reindexing}>
                {reindexing ? <Loader2 className="animate-spin" aria-hidden="true" /> : <RefreshCw aria-hidden="true" />}
                {reindexing ? '重新切分中' : '重新切分'}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setConfirmingDelete(true)} disabled={deleting}>
                <Trash2 aria-hidden="true" />
                删除
              </Button>
            </div>
          )}

          {confirmingDelete ? (
            // 二次确认做成内联区块而不是 window.confirm：
            // 后者在测试环境要 mock，也无法把"会删掉什么"说清楚。
            <div role="alertdialog" aria-label="确认删除文档" className="border-destructive/40 bg-destructive/5 space-y-2 rounded-lg border p-3">
              <p className="text-sm font-medium">确定删除《{doc.title}》？</p>
              <p className="text-muted-foreground text-xs">
                文档、{doc.chunk_count} 个分块与原始文件会一并移除，无法恢复。
              </p>
              <div className="flex items-center gap-2">
                <Button variant="destructive" size="sm" onClick={onDelete} disabled={deleting}>
                  {deleting ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
                  确认删除
                </Button>
                <Button variant="ghost" size="sm" onClick={() => setConfirmingDelete(false)} disabled={deleting}>
                  取消
                </Button>
              </div>
            </div>
          ) : null}
        </CardContent>
      </Card>

      {chunksLoading ? (
        <div aria-busy="true" className="space-y-3">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : (
        <ChunkList chunks={chunks} total={chunkTotal} maxReturned={CHUNK_PAGE_SIZE} />
      )}
    </div>
  )
}

function MetadataRow({ doc }: { doc: DocumentView }) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <span className="text-muted-foreground">分类</span>
      {doc.collection === '' ? (
        <span className="text-muted-foreground">未分类</span>
      ) : (
        <Badge variant="secondary">{doc.collection}</Badge>
      )}
      <span className="text-muted-foreground ml-2">标签</span>
      {doc.tags.length === 0 ? (
        <span className="text-muted-foreground">无</span>
      ) : (
        doc.tags.map((tag) => (
          <Badge key={tag} variant="outline" className="font-normal">
            {tag}
          </Badge>
        ))
      )}
    </div>
  )
}

function MetadataForm({
  doc,
  saving,
  onSubmit,
  onCancel,
}: {
  doc: DocumentView
  saving: boolean
  onSubmit: (patch: DocumentPatch) => void
  onCancel: () => void
}) {
  // 初值来自当前文档；因为父级用 key={doc.id} 渲染，换文档时这里会重新初始化。
  const [title, setTitle] = useState(doc.title)
  const [collection, setCollection] = useState(doc.collection)
  const [tags, setTags] = useState(doc.tags.join(', '))

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    // 只传真正发生变化的字段：PATCH 的语义是"改我传的"，
    // 全量提交会把用户没动过的字段也用当前值覆盖一遍，徒增误改风险。
    const patch: DocumentPatch = {}
    if (title.trim() !== doc.title) {
      patch.title = title.trim()
    }
    if (collection.trim() !== doc.collection) {
      patch.collection = collection.trim()
    }
    const nextTags = splitTags(tags)
    if (nextTags.join(',') !== doc.tags.join(',')) {
      patch.tags = nextTags
    }
    if (Object.keys(patch).length === 0) {
      onCancel()
      return
    }
    onSubmit(patch)
  }

  return (
    <form className="space-y-3" onSubmit={submit}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Input value={title} onChange={(e) => setTitle(e.target.value)} aria-label="文档标题" maxLength={200} />
        <Input
          value={collection}
          onChange={(e) => setCollection(e.target.value)}
          placeholder="分类（留空表示未分类）"
          aria-label="文档分类"
          maxLength={64}
        />
      </div>
      <Input
        value={tags}
        onChange={(e) => setTags(e.target.value)}
        placeholder="标签（逗号分隔，留空表示清空）"
        aria-label="文档标签"
      />
      <div className="flex items-center gap-2">
        <Button type="submit" size="sm" disabled={saving}>
          {saving ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
          保存
        </Button>
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={saving}>
          取消
        </Button>
      </div>
    </form>
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
