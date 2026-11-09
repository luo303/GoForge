import { Filter, X } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import type { DocumentFilters, DocumentStatus } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'

import { statusOptions } from './format'

const emptyFilters: DocumentFilters = { collection: '', tag: '', status: '' }

/**
 * 列表筛选栏。
 *
 * 条件是"点筛选才生效"而不是随输入变化：每敲一个字就发一次请求既浪费，
 * 也会让列表在输入过程中反复跳动。输入中只是草稿，提交后才成为查询条件。
 */
export function FilterBar({
  filters,
  onChange,
}: {
  filters: DocumentFilters
  onChange: (next: DocumentFilters) => void
}) {
  const [collection, setCollection] = useState(filters.collection)
  const [tag, setTag] = useState(filters.tag)
  const [status, setStatus] = useState<DocumentStatus | ''>(filters.status)

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    onChange({ collection: collection.trim(), tag: tag.trim(), status })
  }

  const reset = () => {
    setCollection('')
    setTag('')
    setStatus('')
    onChange(emptyFilters)
  }

  const isFiltered = filters.collection !== '' || filters.tag !== '' || filters.status !== ''

  return (
    <Card className="py-3">
      <CardContent>
        <form className="flex flex-wrap items-end gap-2" onSubmit={submit}>
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground">分类</span>
            <Input
              value={collection}
              onChange={(e) => setCollection(e.target.value)}
              placeholder="全部"
              aria-label="按分类筛选"
              className="h-8 w-40"
            />
          </label>
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground">标签</span>
            <Input
              value={tag}
              onChange={(e) => setTag(e.target.value)}
              placeholder="全部"
              aria-label="按标签筛选"
              className="h-8 w-40"
            />
          </label>
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground">状态</span>
            {/* 原生 select：项目没有下拉组件，而这里只需要一个单值选择，
                为它引入一整套 Popover 得不偿失。背景色显式声明，避免看起来"不可点"。 */}
            <select
              value={status}
              onChange={(e) => setStatus(e.target.value as DocumentStatus | '')}
              aria-label="按状态筛选"
              className="border-input bg-background h-8 rounded-md border px-2 text-sm shadow-xs"
            >
              {statusOptions.map((option) => (
                <option key={option.value || 'all'} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          </label>
          <Button type="submit" variant="outline" size="sm">
            <Filter aria-hidden="true" />
            筛选
          </Button>
          {isFiltered ? (
            <Button type="button" variant="ghost" size="sm" onClick={reset}>
              <X aria-hidden="true" />
              清除
            </Button>
          ) : null}
        </form>
      </CardContent>
    </Card>
  )
}
