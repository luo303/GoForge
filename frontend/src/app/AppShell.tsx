import { Menu, X } from 'lucide-react'
import { useState } from 'react'
import { NavLink, Outlet } from 'react-router'

import { modules } from '@/app/modules'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

/**
 * 应用外壳：左侧导航 + 主工作区。
 *
 * 窄屏（< md）不挤压内容，而是把导航收进抽屉——六个模块的侧边栏在手机上
 * 会吃掉一半宽度，宁可多点一次菜单。抽屉的开关状态只属于布局，不进全局 store。
 */
export function AppShell() {
  const [drawerOpen, setDrawerOpen] = useState(false)
  const closeDrawer = () => setDrawerOpen(false)

  return (
    <div className="flex h-full">
      <aside className="hidden shrink-0 md:block">
        <SideNav />
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="bg-background/95 flex h-14 shrink-0 items-center gap-3 border-b px-4 backdrop-blur md:hidden">
          <Button variant="ghost" size="icon" aria-label="打开导航" onClick={() => setDrawerOpen(true)}>
            <Menu aria-hidden="true" />
          </Button>
          <span className="font-semibold tracking-tight">MindOS</span>
        </header>

        <main className="min-w-0 flex-1 overflow-y-auto px-4 py-6 md:px-8 md:py-8">
          <Outlet />
        </main>
      </div>

      {drawerOpen ? (
        <div className="fixed inset-0 z-50 md:hidden" role="dialog" aria-modal="true" aria-label="导航抽屉">
          {/* 遮罩用按钮实现：点击即关闭，也天然可被键盘聚焦 */}
          <button
            type="button"
            aria-label="关闭导航"
            className="absolute inset-0 bg-black/40"
            onClick={closeDrawer}
          />
          <div className="absolute inset-y-0 left-0 shadow-xl">
            <SideNav onNavigate={closeDrawer} />
            <Button
              variant="ghost"
              size="icon"
              aria-label="关闭导航"
              className="absolute top-2 right-2"
              onClick={closeDrawer}
            >
              <X aria-hidden="true" />
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  )
}

function SideNav({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <div className="bg-sidebar text-sidebar-foreground border-sidebar-border flex h-full w-60 flex-col border-r">
      <div className="flex h-14 items-center gap-2 px-5">
        <img src="/favicon.svg" alt="" className="size-6 rounded-md" />
        <span className="font-semibold tracking-tight">MindOS</span>
      </div>

      <nav aria-label="主导航" className="flex-1 space-y-1 px-3 py-2">
        {modules.map((item) => (
          <NavLink
            key={item.key}
            to={item.path}
            // 只有根路径需要 end：不加的话每个页面都会匹配到 "/" 并高亮工作台
            end={item.path === '/'}
            onClick={onNavigate}
            className={({ isActive }) =>
              cn(
                'flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors',
                isActive
                  ? 'bg-sidebar-accent text-sidebar-accent-foreground font-medium'
                  : 'text-muted-foreground hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground',
              )
            }
          >
            <item.icon className="size-4 shrink-0" aria-hidden="true" />
            {item.label}
          </NavLink>
        ))}
      </nav>

      <p className="text-muted-foreground px-5 pb-4 text-xs">本地单用户 · Phase 3</p>
    </div>
  )
}
