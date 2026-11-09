import { useQuery } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import type { ReactNode } from 'react'

import { describeError } from '@/api/errors'
import { fetchSettings, fetchSystemStatus, queryKeys } from '@/api/system'
import type {
  DependencyState,
  MigrationState,
  MigrationStatus,
  ModelSettings,
  SettingsView,
  SystemStatus,
} from '@/api/types'
import { ErrorState } from '@/components/error-state'
import { PageHeader } from '@/components/page-header'
import { StatusPill } from '@/components/status-pill'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

type Tone = 'ok' | 'warn' | 'danger' | 'muted'
type PillMeta = { label: string; tone: Tone }

const dependencyPill: Record<DependencyState, PillMeta> = {
  up: { label: '正常', tone: 'ok' },
  down: { label: '不可用', tone: 'danger' },
  missing: { label: '缺失', tone: 'warn' },
  unknown: { label: '未知', tone: 'muted' },
}

const migrationPill: Record<MigrationState, PillMeta> = {
  ok: { label: '已是最新', tone: 'ok' },
  behind: { label: '版本落后', tone: 'warn' },
  dirty: { label: '中断未恢复', tone: 'danger' },
  not_initialized: { label: '未初始化', tone: 'warn' },
  unknown: { label: '未知', tone: 'muted' },
}

/**
 * 设置页（Phase 3 只读版）：真实读取 /system/status 与 /settings。
 *
 * 两个请求分开管理而不是合成一个：一个挂了不该让另一半也空白，
 * 页面上也能明确指出是哪一块读不到。
 */
export function SettingsPage() {
  const statusQuery = useQuery({
    queryKey: queryKeys.systemStatus,
    queryFn: ({ signal }) => fetchSystemStatus(signal),
  })
  const settingsQuery = useQuery({
    queryKey: queryKeys.settings,
    queryFn: ({ signal }) => fetchSettings(signal),
  })

  const retry = () => {
    void statusQuery.refetch()
    void settingsQuery.refetch()
  }
  const retrying = statusQuery.isFetching || settingsQuery.isFetching

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
      <PageHeader
        title="设置"
        description="系统依赖状态、迁移版本与只读配置视图。"
        actions={
          <Button variant="outline" size="sm" onClick={retry} disabled={retrying}>
            <RefreshCw aria-hidden="true" />
            刷新状态
          </Button>
        }
      />

      {statusQuery.isPending ? (
        <LoadingCard title="系统状态" />
      ) : statusQuery.error ? (
        <ErrorState
          title="读取系统状态失败"
          description={describeError(statusQuery.error)}
          onRetry={retry}
          retrying={retrying}
        />
      ) : (
        <SystemStatusCard status={statusQuery.data} />
      )}

      {settingsQuery.isPending ? (
        <LoadingCard title="运行配置" />
      ) : settingsQuery.error ? (
        <ErrorState
          title="读取配置失败"
          description={describeError(settingsQuery.error)}
          onRetry={retry}
          retrying={retrying}
        />
      ) : (
        <SettingsCard settings={settingsQuery.data} />
      )}

      <p className="text-muted-foreground text-xs">
        配置只读：修改需要编辑 backend/.env 后重启服务。密钥一律只显示脱敏值，完整内容不会出现在浏览器里。
      </p>
    </div>
  )
}

function SystemStatusCard({ status }: { status: SystemStatus }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>系统状态</CardTitle>
        <CardDescription>后端版本 {status.version}，数据来自 GET /api/v1/system/status</CardDescription>
      </CardHeader>
      <CardContent className="divide-y">
        <StatusRow
          label="PostgreSQL"
          detail={status.postgres.detail}
          pill={<StatusPill tone={dependencyPill[status.postgres.status].tone}>{dependencyPill[status.postgres.status].label}</StatusPill>}
        />
        <StatusRow
          label="pgvector 扩展"
          detail={status.pgvector.detail}
          pill={<StatusPill tone={dependencyPill[status.pgvector.status].tone}>{dependencyPill[status.pgvector.status].label}</StatusPill>}
        />
        <StatusRow
          label="数据库迁移"
          detail={migrationDetail(status.migration)}
          pill={<StatusPill tone={migrationPill[status.migration.state].tone}>{migrationPill[status.migration.state].label}</StatusPill>}
        />
      </CardContent>
    </Card>
  )
}

function migrationDetail(migration: MigrationStatus): string {
  const versions = `当前版本 ${migration.current_version} / 代码最新 ${migration.latest_version}`
  return migration.detail ? `${versions} · ${migration.detail}` : versions
}

function SettingsCard({ settings }: { settings: SettingsView }) {
  const tokenBudget = settings.agent.token_budget.toLocaleString('zh-CN')

  return (
    <Card>
      <CardHeader>
        <CardTitle>运行配置</CardTitle>
        <CardDescription>来自后端 .env 的只读视图，密码字段不返回</CardDescription>
      </CardHeader>
      <CardContent>
        <dl className="grid gap-4 sm:grid-cols-2">
          <Field label="运行环境" value={settings.app.env} />
          <Field label="监听地址" value={settings.app.http_addr} />
          <Field label="日志" value={`${settings.log.level} · ${settings.log.format}`} />
          <Field label="数据库" value={`${settings.db.host}:${settings.db.port}/${settings.db.name}`} />
          <Field label="数据库用户" value={settings.db.user} />
          <Field label="存储目录" value={settings.storage.dir} />
          <Field
            label="单次导入上限"
            value={`${(settings.storage.max_upload_bytes / 1024 / 1024).toFixed(0)} MiB`}
          />
          <ModelField label="LLM" model={settings.llm} />
          <ModelField label="Embedding" model={settings.embedding} />
          <Field label="Agent 步数上限" value={String(settings.agent.max_steps)} />
          <Field label="Agent token 预算" value={tokenBudget} />
          <Field label="单次运行超时" value={`${settings.agent.run_timeout_seconds}s`} />
        </dl>
      </CardContent>
    </Card>
  )
}

function ModelField({ label, model }: { label: string; model: ModelSettings }) {
  const key = model.api_key_configured ? model.api_key_masked : '未配置'
  return <Field label={label} value={`${model.model || '未配置模型'} · ${key}`} />
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="space-y-0.5">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="text-sm font-medium break-all">{value}</dd>
    </div>
  )
}

function StatusRow({ label, detail, pill }: { label: string; detail?: string; pill: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 py-3 first:pt-0 last:pb-0">
      <div className="min-w-0 space-y-0.5">
        <p className="text-sm font-medium">{label}</p>
        {detail ? <p className="text-muted-foreground text-xs break-all">{detail}</p> : null}
      </div>
      {pill}
    </div>
  )
}

function LoadingCard({ title }: { title: string }) {
  return (
    <Card aria-busy="true">
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription className="sr-only">正在加载</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
        <Skeleton className="h-4 w-3/5" />
      </CardContent>
    </Card>
  )
}
