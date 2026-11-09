import { Loader2 } from 'lucide-react'
import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

import { splitTags } from './format'

export type NoteDraft = {
  title: string
  content: string
  collection: string
  tags: string[]
}

/**
 * 新建笔记表单。
 *
 * 标题留空时由后端取正文第一行：这个规则写在提示文案里，
 * 否则用户会以为"不填标题"等于"没有标题"。
 */
export function NoteComposer({
  saving,
  onSubmit,
  onCancel,
}: {
  saving: boolean
  onSubmit: (draft: NoteDraft) => void
  onCancel: () => void
}) {
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [collection, setCollection] = useState('')
  const [tags, setTags] = useState('')

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (content.trim() === '') {
      return
    }
    onSubmit({
      title: title.trim(),
      content,
      collection: collection.trim(),
      tags: splitTags(tags),
    })
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
          <div className="grid gap-3 sm:grid-cols-2">
            <Input
              value={collection}
              onChange={(e) => setCollection(e.target.value)}
              placeholder="分类（可选，例如 工作）"
              aria-label="笔记分类"
              maxLength={64}
            />
            <Input
              value={tags}
              onChange={(e) => setTags(e.target.value)}
              placeholder="标签（可选，逗号分隔）"
              aria-label="笔记标签"
            />
          </div>
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
