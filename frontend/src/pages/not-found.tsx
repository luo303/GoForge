import { CircleAlert } from 'lucide-react'
import { useNavigate } from 'react-router'

import { EmptyState } from '@/components/empty-state'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'

export function NotFoundPage() {
  const navigate = useNavigate()

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
      <PageHeader title="页面不存在" description="地址可能拼错，或该页面在后续阶段才开放。" />
      <EmptyState
        icon={CircleAlert}
        title="404"
        description="当前路径没有对应页面。"
        // 用按钮 + 编程式跳转，而不是 <button><a/></button>：
        // 交互元素嵌套既不符合 HTML 规范，键盘操作时也会出现两个可聚焦目标。
        action={<Button variant="outline" size="sm" onClick={() => navigate('/')}>返回工作台</Button>}
      />
    </div>
  )
}
