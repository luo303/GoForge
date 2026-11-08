# MindOS Web 应用

本目录存放 MindOS 的前端代码，技术选型在 **Phase 1** 落地（见 `../docs/plan.md`）。

## 技术栈

React + TypeScript + Vite + Tailwind CSS + shadcn/ui + React Router + TanStack Query。

状态管理约定：

- 服务端状态一律走 TanStack Query，不手工往 Redux 里塞接口数据。
- 跨页共享的少量客户端状态（侧边栏折叠、当前会话）用 Context。
- SSE 用原生 `EventSource` 封装成 hook，断线按事件 `seq` 续传。

## 页面

左侧导航 + 主工作区，六页：

| 页面 | 核心内容 |
|---|---|
| Dashboard | 今日待办、目标进度、最近知识、重要记忆、AI 入口 |
| Chat | 会话列表、消息流、执行轨迹侧栏、审批卡片 |
| Knowledge | 文档列表（分类/标签/搜索）、详情、分块预览与检索高亮 |
| Memory | 按 kind 分组、来源与版本、行内编辑与归档 |
| Tasks | 目标 → 计划 → 任务三级视图、看板 |
| Settings | 模型配置、工具开关、系统状态 |

## 约定

- 后端优先：接口不存在时页面显示空态，不造假数据。
- 前后端分离开发，`vite.config.ts` 里配 `/api` 代理到 `localhost:8080`，生产由后端托管静态资源。
- 接口类型从 `docs/design/overview.md` 的 API 清单生成或手写，不散落 `any`。
- 组件只做展示与交互，业务判断留在后端；前端不复制一份权限/状态机逻辑。
