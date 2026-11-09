import { QueryClient } from '@tanstack/react-query'

/**
 * QueryClient 的默认策略。
 *
 * retry 1：后端没起时重试一次足够——再多只会让"失败态"晚 5 秒出现，
 * 而这个项目里失败态本身就是需要给人看的信息。
 * refetchOnWindowFocus false：本地应用切窗口很频繁，没必要每次都重查。
 */
export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: 1,
        staleTime: 15_000,
        refetchOnWindowFocus: false,
      },
    },
  })
}
