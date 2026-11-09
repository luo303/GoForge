import { RefreshCw, TriangleAlert } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Card, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'

type ErrorStateProps = {
  title: string
  description: string
  onRetry?: () => void
  /** 重试请求进行中时禁用按钮，避免连点打出一串请求 */
  retrying?: boolean
}

/**
 * 请求失败态。与空态分开：空态是"确实没有数据"，失败态是"没拿到数据"，
 * 后者必须给出原因和重试入口，否则用户只会以为页面坏了。
 */
export function ErrorState({ title, description, onRetry, retrying = false }: ErrorStateProps) {
  return (
    // role=alert：错误是异步出现的，屏幕阅读器需要主动播报，
    // 否则用户只能靠自己再扫一遍页面才知道请求失败了。
    <Card role="alert" className="border-destructive/40">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <TriangleAlert className="text-destructive size-4" aria-hidden="true" />
          {title}
        </CardTitle>
        <CardDescription className="break-words">{description}</CardDescription>
      </CardHeader>
      {onRetry ? (
        <CardFooter>
          <Button variant="outline" size="sm" onClick={onRetry} disabled={retrying}>
            <RefreshCw aria-hidden="true" />
            重试
          </Button>
        </CardFooter>
      ) : null}
    </Card>
  )
}
