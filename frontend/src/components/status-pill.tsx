import { cn } from '@/lib/utils'

type Tone = 'ok' | 'warn' | 'danger' | 'muted'

const toneClass: Record<Tone, string> = {
  ok: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300',
  warn: 'border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300',
  danger: 'border-rose-500/30 bg-rose-500/10 text-rose-700 dark:text-rose-300',
  muted: 'border-border bg-muted text-muted-foreground',
}

/**
 * 状态标签。颜色只表达"要不要处理"，具体含义由文字说清：
 * 只靠颜色区分状态对色觉障碍用户不可用，所以文案永远和颜色一起出现。
 */
export function StatusPill({ tone, children }: { tone: Tone; children: string }) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium whitespace-nowrap',
        toneClass[tone],
      )}
    >
      {children}
    </span>
  )
}
