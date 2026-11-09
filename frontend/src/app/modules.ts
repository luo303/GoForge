import {
  BookOpen,
  Brain,
  LayoutDashboard,
  ListChecks,
  MessageSquare,
  Settings,
  type LucideIcon,
} from 'lucide-react'

/**
 * 六个模块的唯一事实来源：导航、页面标题、占位说明都从这里取。
 *
 * 为什么不分散写在页面里：导航顺序、路由路径、阶段编号任何一处改动，
 * 只要有多份副本就一定会不同步；集中成一张表后，新增模块只改这里。
 */
export type ModuleKey = 'dashboard' | 'chat' | 'knowledge' | 'memory' | 'tasks' | 'settings'

export type ModuleDef = {
  key: ModuleKey
  path: string
  /** 导航上的短标签 */
  label: string
  /** 页面标题 */
  title: string
  /** 页面副标题：说清这个模块最终要做什么 */
  description: string
  /** 真正接入数据的阶段，占位页直接展示，避免"看起来像坏了" */
  phase: string
  icon: LucideIcon
}

export const modules: readonly ModuleDef[] = [
  {
    key: 'dashboard',
    path: '/',
    label: '工作台',
    title: '工作台',
    description: '今日待办、目标进度、最近知识与重要记忆的汇总入口。',
    phase: 'Phase 17–20',
    icon: LayoutDashboard,
  },
  {
    key: 'chat',
    path: '/chat',
    label: '对话',
    title: '对话',
    description: '与 Agent 的会话列表、消息流与执行轨迹。',
    phase: 'Phase 13–16',
    icon: MessageSquare,
  },
  {
    key: 'knowledge',
    path: '/knowledge',
    label: '知识',
    title: '知识',
    description: '文档入库、分块预览、检索结果与引用定位。',
    phase: 'Phase 4–9',
    icon: BookOpen,
  },
  {
    key: 'memory',
    path: '/memory',
    label: '记忆',
    title: '记忆',
    description: '四类长期记忆的浏览、来源追溯与版本管理。',
    phase: 'Phase 10–12',
    icon: Brain,
  },
  {
    key: 'tasks',
    path: '/tasks',
    label: '任务',
    title: '任务',
    description: '目标 → 计划 → 任务三级视图，依赖与进度。',
    phase: 'Phase 17–18',
    icon: ListChecks,
  },
  {
    key: 'settings',
    path: '/settings',
    label: '设置',
    title: '设置',
    description: '系统依赖状态、迁移版本与只读配置视图。',
    phase: 'Phase 3',
    icon: Settings,
  },
]

export function getModule(key: ModuleKey): ModuleDef {
  const found = modules.find((item) => item.key === key)

  // modules 是常量表，取不到只可能是 key 写错，属于开发期错误，
  // 早抛比渲染出一个空白页面更容易发现。
  if (!found) {
    throw new Error(`unknown module key: ${key}`)
  }
  return found
}
