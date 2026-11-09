import { getModule, type ModuleKey } from '@/app/modules'
import { EmptyState } from '@/components/empty-state'
import { PageHeader, PhaseBadge } from '@/components/page-header'

/**
 * 尚未接入的模块占位。
 *
 * 刻意不摆假数据、假图表：占位页只说明"这里将有什么、什么时候有"。
 * 没有接口就显示"尚未接入"，是 frontend/README 里写死的约定。
 */
export function ModulePlaceholder({ moduleKey }: { moduleKey: ModuleKey }) {
  const item = getModule(moduleKey)

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
      <PageHeader title={item.title} description={item.description} actions={<PhaseBadge phase={item.phase} />} />
      <EmptyState
        icon={item.icon}
        title="尚未接入"
        description={`${item.label}模块将在 ${item.phase} 落地。当前页面只验证导航与布局，不展示任何模拟数据。`}
      />
    </div>
  )
}
