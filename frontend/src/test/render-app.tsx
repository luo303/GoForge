import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'

import { routes } from '@/app/router'

/**
 * 按指定路径渲染整个应用（含真实路由表）。
 *
 * 测试里新建 QueryClient 而不是复用生产的单例：用例之间共享缓存会互相污染，
 * 例如上一个用例的成功结果会让下一个用例直接跳过请求。
 * retry 关掉，否则失败用例要等默认重试跑完才看得到错误态。
 */
export function renderApp(initialPath = '/') {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 0 } },
  })
  const router = createMemoryRouter(routes, { initialEntries: [initialPath] })

  const utils = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )

  return { ...utils, queryClient, router }
}
