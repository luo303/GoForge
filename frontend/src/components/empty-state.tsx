import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'

type EmptyStateProps = {
  icon: LucideIcon
  title: string
  description: string
  /** 补充操作，例如"重试"或"去设置" */
  action?: ReactNode
}

/**
 * 统一空态。所有"接口还没实现 / 暂无数据"的位置都复用它，
 * 避免每个页面各画一套，也让"空"和"加载失败"在视觉上明确区分开。
 */
export function EmptyState({ icon: Icon, title, description, action }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-xl border border-dashed px-6 py-14 text-center">
      <div className="bg-muted text-muted-foreground flex size-11 items-center justify-center rounded-full">
        <Icon className="size-5" aria-hidden="true" />
      </div>
      <div className="space-y-1">
        <p className="font-medium">{title}</p>
        <p className="text-muted-foreground mx-auto max-w-md text-sm">{description}</p>
      </div>
      {action}
    </div>
  )
}
