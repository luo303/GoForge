# MindOS 前端开发规范

本文件与根目录 `AGENTS.md` 共同构成 `frontend/` 的准入标准。`AGENTS.md` 管边界与流程，本文件管代码怎么写。
与代码冲突时：**要么改代码，要么改本文件**，不允许长期不一致。

来源：项目原有前端规范（AI 代码开发规范 + Style Guide）适配本仓库后摘选。**未收录的条目与理由在最后一节**——那是判断结果，不是遗漏。

**优先级**（冲突时自上而下取舍）：

1. 不破坏既有行为
2. 不引入渲染副作用
3. 不违反 Hooks 规则
4. 类型边界清晰
5. 可读性优先于炫技
6. 最小改动，不做无关重构
7. 通过 `lint` / `typecheck`
8. 最后才考虑性能优化

## 1. 导出与文件结构

- **只用命名导出，禁止 `default export`**。`react-refresh` 规则也不允许组件文件混着导出非组件，所以变体表（如 `buttonVariants`）保持文件内私有。
- 不建 barrel 文件；确实需要 `index.ts` 时只做转出，不写实现。
- **页面入口保持薄**：`src/pages/*.tsx` 负责取数与编排，成块的视图拆到同目录子文件或子组件。单个组件文件超过 500 行必须拆（`knowledge.tsx` 现为 443 行，已接近上限，再加功能先拆）。
- 只在本页使用的常量与映射表可以留在同文件顶部（如 `statusMeta`），一旦被两处引用或超过约 20 行，就挪到 `lib/` 或同目录的独立文件。
- 不在组件函数内部定义子组件。纯展示、无状态、不进列表的片段可以用 `renderXxx()`；**有状态或被复用两次以上，抽成独立组件**。
- 大块 JSX 拆开，不写嵌套三层以上的条件渲染。

## 2. 样式

- **Tailwind 优先**，且本项目只有 Tailwind：不引入 `*.module.less`、CSS-in-JS、`styled-components`。主题写在 `src/index.css` 的 `@theme`，不在组件里堆内联样式。
- 基础控件用 `@/components/ui/` 里的 shadcn 组件，不自己拼一套按钮/输入框。
- **不直接写原生 `<button>`**：Tailwind 的 preflight 清掉了默认背景，裸 `<button>` 会出现"看起来不能点"的无底色按钮。确有需要（例如整张卡片作为点击区）必须显式声明 `bg-*`，含 `hover:` 与 `disabled:` 状态，并在浏览器里核对过实际观感，不能只看 JSX。
- 颜色与间距用设计令牌（`bg-background` / `text-muted-foreground` / `border-border`），不写死 `#hex`。

## 3. React 渲染纪律

- **render 阶段必须纯净**：不读写 `ref.current`，不修改 props、外部变量、全局变量，不发起请求。
- render 阶段禁止无条件 `setState`。确实需要"条件复位"时（props 变了要把内部状态归零），只在值真的变化时设一次；**不要改用 `useEffect` 同步 `setState`**，那会触发 cascading render，也是本仓库目前零 `useEffect` 的原因之一。
- **能直接算的值不要用 `useEffect + setState`**：`const activeId = selectedId ?? items[0]?.id ?? null` 就够了，别用状态镜像它。
- `useEffect` 只用于同步外部系统（订阅、DOM 测量、第三方实例）。事件行为写在事件回调里，不绕到 effect 里。
- 本项目服务端状态一律走 TanStack Query，**不复制进本地 `useState`**；请求的取消由 Query 传下来的 `signal` 负责，不手写竞态判断。
- Hooks 只在组件顶层调用，禁止条件调用；**禁止 `eslint-disable` 绕过 hooks 规则**。
- `useMemo` 只做纯计算，不许在里面 `setState`；不要为"优化"随手加 `useMemo` / `useCallback`，`useCallback` 的返回值不能当组件用。
- 列表 `key` 必须稳定且唯一，**不要用数组下标**。同一父节点下不同操作不能用同一个业务 ID 作 key：改名用 `rename-${id}`、导出用 `export-${id}`，否则切换目标时会累积 DOM。改完必须实测多次切换。

## 4. TypeScript

- props、hooks 入参与返回值、公共函数入参与返回值、store/context、API 响应，**一律禁止 `any`**；外部输入不确定时用 `unknown` 再收窄。
- `any` 只能作为函数内部的一次性过渡，不能 return、不能存进状态、不能传给公共 API。
- 不用 `object` 表达业务对象，写明确的 `interface` / `type`；类型定义放在文件顶层，**不在函数体内定义**。
- `null` 与 `undefined` 要区分：默认参数只对 `undefined` 生效，传 `null` 不会走默认值——`apiRequest(path, { method = 'GET' })` 这类签名要意识到这一点。
- 必填却没用到的字段要删掉或改可选；`tsconfig` 已开 `noUnusedLocals` / `noUnusedParameters`，不要用下划线绕过。
- `verbatimModuleSyntax` 打开，**类型导入必须写 `import type`**。
- 接口类型是手写镜像后端 JSON（`src/api/types.ts`），保持 snake_case，不引入 camelCase 转换层；后端改字段时同步这里。

## 5. 命名与变量

- 默认 `const`，需要重新赋值才 `let`，**禁止 `var`**。
- 命名带业务语义：`documentTotal` 而不是 `data`，`chunkList` 而不是 `list`。禁止 `a` / `b` / `v` / `temp` / `v1`。
- 布尔值用 `is` / `has` / `should` / `can` / `need` 前缀；事件处理函数用 `handle` 前缀；渲染函数用 `render` 前缀。
- 删除无用变量、无用赋值、无用参数——`noUnusedLocals` 已经会拦，别用注释关掉。

## 6. 条件与数据结构

- 复杂条件拆成语义变量：`const isFirstPage = page === 1`。
- **`&&` 只用于条件渲染**（`{items.length > 0 && <List />}`），不用于执行业务逻辑；业务分支一律 `if`。
- 不用三元表达式做业务动作，不用三层嵌套三元，不在 JSX 里塞巨型三元块。
- 魔法数字、状态码、模式值必须是常量或映射表（`PAGE_SIZE`、`CHUNK_PAGE_SIZE`、`statusMeta`）。
- 兼容老数据的判断必须写注释说明兼容的是什么。
- `map` 只用于产出新数组，不产出新数组用 `forEach`；`filter` 回调必须返回 boolean。
- **不直接修改数组、对象、嵌套配置**，更新时创建新对象（`{ ...doc, status: 'ready' }`）。
- 复杂筛选逻辑封成语义函数，不把三四个条件堆在 JSX 里。

## 7. 异步与副作用

- 异步结果通过返回值传递，不顺手改外部作用域变量。
- 请求、上传、定时器、订阅都要考虑清理与竞态；状态更新前确认当前请求仍有效（Query 的 `signal` 已经处理大半，别再叠一层手写逻辑）。
- 不滥用 `window` 自定义事件；跨模块通信优先走 Query 缓存失效或状态提升。
- **不提交临时调试代码**（`console.log`、写死的测试数据）；但**不要删别人已有的调试输出**，除非明确要求清理。

## 8. 修改流程（对 AI 同样适用）

1. 先读上下文再改代码，先判断问题类型：lint / 类型 / hooks / 渲染 / 副作用 / 业务语义。
2. 优先做最小、安全、语义清晰的修复；不扩大历史问题，不为修 lint 引入新风险。
3. 改完说明：改了什么、为什么、是否影响行为。
4. 跑对应门禁：`npm run lint` + `npm run test` + `npm run build`（`build` 内含 `tsc --noEmit`）。未验证要说明原因。
5. **只写或补测试时，禁止改源码**。测试挂了先报原因，未经明确要求不得为了让测试通过而改实现。
6. 不写 `eslint-disable` 逃避问题；真需要时先讨论规则本身是否该调整。

## 9. 测试文件位置

**测试不与被测代码混放，一律进对应目录下的 `test/` 子目录**：

```text
src/
├── api/
│   ├── client.ts
│   ├── documents.ts
│   └── test/
│       └── client.test.ts        # 对应用 api/client.ts
├── pages/
│   ├── knowledge.tsx
│   └── test/
│       └── knowledge.test.tsx
└── test/                         # 跨模块共享的测试基础设施，不放业务用例断言
    ├── setup.ts
    ├── fixtures.ts
    ├── render-app.tsx
    └── placement.test.ts
```

- 命名保持 `<被测模块>.test.ts(x)`，与源文件同名，靠目录区分身份。
- `src/test/` 只放**共享**设施（setup、fixtures、渲染工具、约定自检）；只被一个用例用的辅助函数放在该用例旁边。
- 新模块新建测试：先建 `test/` 目录再写文件，不要先丢在业务目录里"稍后再挪"。
- **这条规则由测试强制执行**：`src/test/placement.test.ts` 会扫描 `src/`，发现任何不在 `test/` 目录下的 `*.test.ts(x)` 就让 `npm run test` 变红。想绕过只有两个选择——挪文件，或删掉那条自检（删之前先问，别默默删）。
- 后端 Go **不适用本条**：`*_test.go` 必须与被测包同目录同包，这是语言规则（同包才能访问未导出标识符，`go test` 也按包发现测试），挪到子目录就编译不过或不覆盖内部实现。

## 10. 未收录的条目与理由

| 原规范条目 | 处理 | 理由 |
|---|---|---|
| 监听数据变化用 `useWatchValue` | 不收录 | 本项目没有这个 hook；派生值直接算，值变化用 Query 失效处理 |
| 配置面板走统一 GUI / schema 渲染 | 不收录 | 那是 EasyV 平台侧的能力；本项目界面是手写页面，没有配置 schema 体系 |
| `*.module.less` 作为 Tailwind 的退路 | 不收录 | 仓库里不存在 Less，引入即多一套构建链 |
| 统一事件中心 + 类型化 payload | 暂不收录 | 目前没有跨模块事件需求；出现时再定，不预先搭架子 |
| 大屏 AI 弹窗 `!bg-*` 深色按钮 | 只保留结论 | 已抽象为"原生 button 必须显式声明背景"，具体样式与本项目无关 |
| 组件文件尽量只导出组件 | 收敛为"只在本页使用的常量可留在文件顶部" | 硬拆会为 3 行映射表多建一个文件，收益低于成本 |

## 11. 提交前自查

- 有没有 `default export`、`any`、`eslint-disable`、`console.log`？
- 新增的列表渲染有没有用下标的 `key`？同一父节点的不同操作用的是不是复合 key？
- 有没有把服务端数据复制进 `useState`，或为派生值加了 `useEffect`？
- 新写的原生 `<button>` 有没有显式背景色？
- 新增的测试文件是不是放在对应目录的 `test/` 下？
- `npm run lint` / `test` / `build` 是否都过？
