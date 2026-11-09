import { QueryClientProvider } from '@tanstack/react-query'
import { useState } from 'react'
import { RouterProvider } from 'react-router'

import { createQueryClient } from '@/app/query-client'
import { router } from '@/app/router'

export function App() {
  // 用 useState 的惰性初始化而不是模块级常量：StrictMode 下组件会挂载两次，
  // 惰性初始化保证两次拿到的是同一个 client，也不会每次渲染都新建缓存。
  const [queryClient] = useState(createQueryClient)

  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}
