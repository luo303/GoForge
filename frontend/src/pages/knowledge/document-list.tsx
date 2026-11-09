import { ChevronLeft, ChevronRight } from 'lucide-react'

import type { DocumentView } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'

import { formatDate, PAGE_SIZE, sourceLabel } from './format'

/**
 * 文档列表。
 *
 * 只负责渲染与"点了哪一条"，取数与分页状态都留在页面入口：
 * 列表组件一旦自己取数，就和页面的筛选条件、选中态脱钩，容易各查各的。
 */
export function DocumentList({
  items,
  total,
  page,
  activeId,
  onSelect,
  onPageChange,
}: {
  items: DocumentView[]
  total: number
  page: number
  activeId: number | null
  onSelect: (id: number) => void
  onPageChange: (page: number) => void
}) {
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  return (
    <div className="space-y-3">
      <Card className="overflow-hidden py-0">
        <ul className="divide-y">
          {items.map((doc) => (
            <li key={doc.id}>
              <button
                type="button"
                onClick={() => onSelect(doc.id)}
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
                <DocTagRow doc={doc} />
              </button>
            </li>
          ))}
        </ul>
      </Card>

      {total > PAGE_SIZE ? (
        <div className="flex items-center justify-between gap-2">
          <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>
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
            onClick={() => onPageChange(page + 1)}
          >
            下一页
            <ChevronRight aria-hidden="true" />
          </Button>
        </div>
      ) : null}
    </div>
  )
}

/** 列表里最多展示 3 个标签，其余折成 "+n"：再多的标签在窄栏里无法阅读。 */
const MAX_VISIBLE_TAGS = 3

function DocTagRow({ doc }: { doc: DocumentView }) {
  const visibleTags = doc.tags.slice(0, MAX_VISIBLE_TAGS)
  const hiddenCount = doc.tags.length - visibleTags.length
  if (doc.collection === '' && doc.tags.length === 0) {
    return null
  }
  return (
    <span className="flex flex-wrap items-center gap-1">
      {doc.collection !== '' ? <Badge variant="secondary">{doc.collection}</Badge> : null}
      {visibleTags.map((tag) => (
        <Badge key={tag} variant="outline" className="font-normal">
          {tag}
        </Badge>
      ))}
      {hiddenCount > 0 ? <span className="text-muted-foreground text-xs">+{hiddenCount}</span> : null}
    </span>
  )
}
