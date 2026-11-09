import { createBrowserRouter, type RouteObject } from 'react-router'

import { AppShell } from '@/app/AppShell'
import { ChatPage } from '@/pages/chat'
import { DashboardPage } from '@/pages/dashboard'
import { KnowledgePage } from '@/pages/knowledge'
import { MemoryPage } from '@/pages/memory'
import { NotFoundPage } from '@/pages/not-found'
import { SettingsPage } from '@/pages/settings'
import { TasksPage } from '@/pages/tasks'

/**
 * 路由表单独导出：生产用 createBrowserRouter，测试用 createMemoryRouter，
 * 两者共享同一份 routes，测试覆盖的就是线上真实配置。
 */
export const routes: RouteObject[] = [
  {
    path: '/',
    element: <AppShell />,
    children: [
      { index: true, element: <DashboardPage /> },
      { path: 'chat', element: <ChatPage /> },
      { path: 'knowledge', element: <KnowledgePage /> },
      { path: 'memory', element: <MemoryPage /> },
      { path: 'tasks', element: <TasksPage /> },
      { path: 'settings', element: <SettingsPage /> },
      // 通配放在最内层：保留导航外壳，用户还能直接点回别的页面
      { path: '*', element: <NotFoundPage /> },
    ],
  },
]

export const router = createBrowserRouter(routes)
