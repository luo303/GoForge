# MindOS 前端

React 单页应用，开发期跑在 5173，`/api` 请求代理到本地 Go 服务 8080。生产同源托管是 Phase 21 的事。

## 技术栈

| 层 | 选型 | 说明 |
|---|---|---|
| 构建 | Vite 8 | 无 SSR，纯静态产物 |
| 框架 | React 19 + TypeScript | `strict` 开启，`verbatimModuleSyntax` 强制 `import type` |
| 样式 | Tailwind CSS v4 | 无 `tailwind.config.js`，主题写在 `src/index.css` 的 `@theme` |
| 组件 | shadcn/ui（复制式） | 只引入用到的 Button / Card / Badge / Skeleton，代码在 `src/components/ui/` |
| 路由 | React Router 8 | `createBrowserRouter`，路由表在 `src/app/router.tsx` |
| 数据 | TanStack Query 5 | 服务端状态一律走 Query，不复制进本地 state |
| 测试 | Vitest 5 + Testing Library | jsdom 环境，`src/test/` 放 setup 与工具 |

**环境**：Node 20+（本项目在 WSL 内用 nvm 的 v24）。nvm 只在交互式 shell 生效，IDE 任务或 `bash -c` 里需要显式加 PATH：

```bash
export PATH=$HOME/.nvm/versions/node/v24.22.1/bin:$PATH
```

## 目录结构

```text
frontend/
├── src/
│   ├── api/            统一客户端、错误模型、接口类型镜像
│   ├── app/            App 组合根、路由表、应用外壳、模块元数据
│   ├── components/     业务无关组件（空态、错误态、状态标签）
│   │   └── ui/         shadcn/ui 基础组件
│   ├── lib/            cn() 等纯工具
│   ├── pages/          六个页面 + 404
│   └── test/           setup、fixtures、renderApp
└── vite.config.ts      Tailwind 插件、@ 别名、/api 代理、Vitest 配置
```

## 命令

```bash
npm install          # 首次
npm run dev          # 开发服务器 http://localhost:5173
npm run lint         # ESLint（扁平配置）
npm run test         # Vitest 单次运行
npm run test:watch   # 监听模式
npm run build        # tsc --noEmit && vite build，产物在 dist/
```

三项门禁（lint / test / build）必须同时通过才算阶段完成。

## 开发约定

**编码规范（导出、渲染纪律、类型、命名、提交前自查）见 [CONVENTIONS.md](CONVENTIONS.md)。** 下面只写本项目特有的约定：数据怎么取、错误怎么表达、页面怎么加。

**数据**

- 服务端状态只走 TanStack Query；查询键集中在 `src/api/system.ts` 的 `queryKeys`，不散写。
- 接口类型在 `src/api/types.ts` 手工镜像后端 JSON，字段保持 snake_case，不引入 camelCase 转换层。
- 后端接口没实现就显示"尚未接入"，**不摆模拟数据、假图表**。
- 失败态必须写出原因并提供重试入口；空态与失败态分开表达。

**错误**

- 所有请求经 `apiRequest`，它在唯一一处解信封 `{ code, message, data, request_id }`。
- 失败抛 `ApiError`，`kind` 区分 `network` / `timeout` / `aborted` / `http` / `business` / `malformed`，不要靠 `message` 字符串判断。
- 展示用 `describeError(error)`，有 `request_id` 时会附在文案里，方便对后端日志。

**页面与路由**

- 模块的路径、标题、阶段只在 `src/app/modules.ts` 定义一份，导航与页面共用。
- 新增页面：`modules.ts` 加一条 → `src/pages/` 建组件 → `router.tsx` 挂到 `AppShell` 的 children → 在 `app.test.tsx` 里补一条可见性用例。
- 窄屏导航收进抽屉（`< md`），主内容区不被侧边栏挤压。
- 知识页是第一个真实业务页：上传走 `FormData`（客户端只在 `apiRequest` 一处判断，不手写 `Content-Type`），分块偏移是**码点**而不是 UTF-16 下标；需要高亮原文时用 `Array.from(text).slice(start, end)`，不要用 `String.slice`。

## 测试

```bash
npm run test                          # 全部
npx vitest run src/pages/settings.test.tsx   # 单个文件
```

- `src/test/render-app.tsx` 用真实路由表 + 独立 QueryClient 渲染整页，路径由参数指定。
- `src/test/fixtures.ts` 提供信封、状态与配置的固定数据，避免每个用例手写一大坨 JSON。
- 请求用 `vi.stubGlobal('fetch', ...)` 替换，不启真实后端；`afterEach` 里 `vi.unstubAllGlobals()`。
- 覆盖重点：成功解析、六类失败分支、加载态、空态、重试后恢复。

## 当前限制

- 设置页只读：改配置要改 `backend/.env` 并重启，前端不提供写接口。
- 知识页只做导入与预览：没有删除、重命名、重新索引，也没有检索框（Phase 5–8 逐步接入）；分块一次最多显示 100 条。
- 未做主题切换；`.dark` 变量已备好，等有真实需求再接。
- 生产构建产物尚未由后端托管，Phase 21 处理。
