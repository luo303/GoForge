import type { ReactNode } from 'react'

import { Badge } from '@/components/ui/badge'

type PageHeaderProps = {
  title: string
  description: string
  /** 右侧附加信息，例如阶段标记或刷新按钮 */
  actions?: ReactNode
}

export function PageHeader({ title, description, actions }: PageHeaderProps) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        <p className="text-muted-foreground text-sm">{description}</p>
      </div>
      {actions ? <div className="flex items-center gap-2">{actions}</div> : null}
    </header>
  )
}

export function PhaseBadge({ phase }: { phase: string }) {
  return <Badge variant="outline">{phase}</Badge>
}
