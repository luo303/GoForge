# MindOS 开发计划

计划粒度到**提交**：每次提交只做一件事，可独立回滚。阶段之间不允许携带未完成的半成品。

通用收尾门槛（每个阶段都必须满足）：

```bash
cd backend
go build ./...
go vet ./...
go test -race -count=1 ./...
```

外加该阶段声明的 curl / 页面 / 日志验证记录，缺一不宣布完成。

阶段与产品能力的对应：P1 骨架 → P2 知识 → P3 记忆 → P4 Agent → P5 行动 → P6 上下文 → P7 洞察 → P8 完善。
**顺序不可调**：Agent 需要知识与记忆当工具，行动需要 Agent 当执行者，上下文优化需要真实的长对话样本。

## Phase 1 工程与基础设施

**目标**：一个能跑、能连库、能打开页面、能改配置的空壳；此后的每个阶段都在这上面长肉。

**交付物**
- Go 模块更名为 `github.com/luo303/mindos/backend`，二进制 `bin/mindos`
- `migrations/`（SQL）+ `cmd/migrate/`（`golang-migrate` 驱动）
- pgvector 扩展启用；PostgreSQL 镜像换 `pgvector/pgvector:pg16`
- `internal/platform/`：PostgreSQL、文件存储根目录
- `internal/config/`：增加 LLM、Embedding、存储、预算四组配置并在启动时校验
- `internal/httpx` 错误码补齐（配置、模型、检索、审批）
- 前端脚手架：Vite + React + TS + Tailwind + shadcn/ui + React Router + TanStack Query，六个页面的空壳与左侧导航
- `GET /system/status`：数据库、向量扩展、模型连通性、迁移版本

**提交拆分**
1. `fix: 修正中间件顺序使 panic 请求保留访问日志`
2. `fix: 配置解析失败时启动报错`（顺带删掉认证遗留配置）
3. `refactor: 模块路径与产品名统一为 MindOS`
4. `feat: 切换 pgvector 镜像并启用向量扩展`
5. `feat: 引入 SQL 迁移与迁移执行入口`
6. `feat: 新增模型、存储与预算配置项`
7. `feat: 新增系统状态接口`（含迁移版本与向量扩展检测）
8. `feat: 前端脚手架与页面骨架`
9. `docs: 更新 README 与本地启动说明`

**验证**
```bash
docker compose --env-file backend/.env -f deployments/docker-compose.yml up -d
cd backend && go run ./cmd/migrate up && go run ./cmd/server
curl -s localhost:8080/system/status | jq
cd frontend && npm run dev   # 六个页面可切换，无报错
```
迁移需验证 `up → down → up` 三次可重复执行。

**退出标准**：`readyz` 显示数据库与 `vector` 扩展均正常；前端能加载布局并请求到 `/system/status`；配置项全部有校验，缺必填项启动即失败。

## Phase 2 Knowledge

**目标**：文档进来能被检索、被引用、被问答。这是整个产品的底座，也是后续记忆与 Agent 的数据来源。

**交付物**
- `internal/knowledge/`：`model.go` / `repository.go` / `service.go` / `handler.go` / `parser.go` / `chunker.go` / `search.go`
- 迁移：`documents`、`document_chunks`（含 `vector(1536)` 与 `tsvector` 生成列、HNSW 与 GIN 索引）
- 解析器：Markdown、纯文本、PDF（文本层）、HTML（正文抽取）
- 切分器：标题优先 → 段落 → token 硬切，512/64，记录偏移
- Embedding 客户端（OpenAI 兼容，批量 64 条，带超时与重试）
- 混合检索 + RRF 融合
- `job` 表与 `worker` 消费 `document_ingest`（Phase 2 先用进程内消费，Phase 4 接 MQ）
- Knowledge 页面：列表、上传、详情、分块预览、检索结果高亮

**提交拆分**
1. `feat: 新增文档与分块模型及迁移`
2. `feat: 实现 Markdown 与纯文本解析及切分`
3. `feat: 实现 PDF 文本层抽取`
4. `feat: 实现网页正文抓取`
5. `feat: 文档上传、笔记与网页导入接口`
6. `feat: 接入 Embedding 客户端与批量写入`
7. `feat: 实现向量与全文混合检索`
8. `feat: 实现文档入库任务与消费`
9. `feat: 基于知识库的 RAG 问答与引用`
10. `feat: 知识库页面`
11. `test: 切分边界、检索融合与引用定位`

**验证**
```bash
curl -F "file=@docs/design/overview.md" localhost:8080/api/v1/documents   # 202
curl -X POST localhost:8080/api/v1/search -d '{"query":"混合检索 RRF"}' | jq '.data.hits[0]'
```
- 同一文件重复上传不产生第二份分块；
- 检索结果能定位到原文偏移，前端点击可滚到对应位置；
- 停掉 Embedding 服务时，任务失败并保留错误码，重试可恢复，不产生半截数据。

**退出标准**：三种来源（文件/笔记/网页）都能入库；检索同时命中"语义相近"和"专名精确"两类查询；RAG 回答的每条引用都能在库中找到对应分块。

## Phase 3 Memory

**目标**：AI 记住用户，并且这份记忆可改、可删、可追溯。

**交付物**
- `internal/memory/`：`model.go` / `repository.go` / `service.go` / `handler.go` / `extractor.go` / `recall.go`
- 迁移：`memories`（含部分唯一索引）、`memory_versions`
- 提炼器：结构化输出 + schema 校验 + evidence 回查
- 合并器：新增 / 合并 / 取代三分支
- 召回打分与 `last_recalled_at` 更新
- Memory 页面：按 kind 分组、来源展示、版本对比、编辑与归档

**提交拆分**
1. `feat: 新增记忆与版本模型及迁移`
2. `feat: 实现记忆提炼的结构化输出与校验`
3. `feat: 实现记忆去重、合并与取代`
4. `feat: 实现记忆召回打分`
5. `feat: 记忆管理与版本查询接口`
6. `feat: 对话结束后触发的记忆提炼任务`（对话在 P4，本阶段先用文档与手动触发验证）
7. `feat: 记忆页面`
8. `test: 合并冲突、部分唯一索引与来源追溯`

**验证**：手动写入"主力栈是 Node.js"→ 再写入"主力栈是 Go" → 前者变 `superseded` 且新记录指向旧 id；查询版本史能看到两次变更；召回接口对"我在学什么"能命中 `profile` 类记忆。

**退出标准**：同一 `(kind, key)` 永远只有一条 `active`；记忆正文不出现对话原文片段；每条记忆都能点开看到来源。

## Phase 4 Agent

**目标**：Agent 能带着知识与记忆回答、调用工具、被中断、被恢复、被审批。

**交付物**
- `internal/chat/`：会话、消息、SSE 推送
- `internal/agent/`：Eino Runner 封装、事件翻译、工具注册、预算、审批门、审计
- 迁移：`conversations`、`messages`、`agent_runs`、`agent_events`、`agent_checkpoints`、`agent_approvals`、`tool_audits`、`jobs`、`outbox_events`、`usage_ledger`、`app_settings`
- RabbitMQ 派发 + Outbox relay + 幂等消费
- 首批只读工具：`search_knowledge`、`read_document`、`search_memory`、`list_tasks`
- 首个写工具：`save_memory`（走审批，验证 HITL 全链路）
- Chat 页面：消息流、执行轨迹侧栏、审批卡片、断线续传

**提交拆分**
1. `feat: 新增会话与消息模型及迁移`
2. `feat: 接入 Eino Runner 与事件翻译`
3. `feat: Agent 事件落库与 SSE 推送`
4. `feat: 注册首批只读工具`
5. `feat: 预算闸门与运行终止`
6. `feat: Checkpoint 存储与断点恢复`
7. `feat: 写工具审批与 Resume`
8. `feat: 工具调用审计`
9. `feat: 接入 RabbitMQ 与 Outbox 中继`
10. `feat: 对话页面`
11. `test: 步数上限、预算耗尽、断连取消、审批恢复`

**验证**
- 问一个知识库里有的问题，回答带引用 `[1]`，点开能看到原文；
- 让 Agent 保存一条记忆，前端出现审批卡片，拒绝后 run 继续且不落库，批准后落库；
- 在"执行中"关闭页面，2 秒内 run 变 `cancelled`，日志显示 goroutine 已退出；
- 把 `budget_tokens` 调到 100，run 以 `BUDGET_EXCEEDED` 结束且不再发起模型调用；
- 批准流程中重启进程，`Resume` 仍能从 checkpoint 继续。

**退出标准**：`-race` 无数据竞争；事件 `seq` 连续无洞；同一 run 不会并发跑两份（用 `agent_runs.status` 条件更新拦住）。

## Phase 5 Action

**目标**：把"我想学 Go 并发"变成一条可执行、可勾选的任务链。

**交付物**
- `internal/action/`：目标、计划、任务、依赖、事件
- 迁移：`goals`、`plans`、`tasks`、`task_dependencies`、`task_events`
- 新工具：`create_goal`、`create_plan`、`create_task`、`update_task`（全部走审批）
- 计划草案 → 用户确认 → 批量落库
- Tasks 页面：三级视图 + 看板 + 拖拽排序
- Dashboard 聚合接口与页面

**提交拆分**
1. `feat: 新增目标、计划与任务模型及迁移`
2. `feat: 任务依赖与环检测`
3. `feat: 计划草案生成与确认落库`
4. `feat: 任务状态流转与事件记录`
5. `feat: 注册行动类写工具并接入审批`
6. `feat: 任务板与今日待办接口`
7. `feat: Tasks 与 Dashboard 页面`
8. `test: 依赖成环、状态流转与 Agent 越权`

**验证**：给出学习目标 → 生成 6–8 条任务草案 → 删掉两条后确认 → 任务落库且带 `plan_id`；把 B 设为依赖 A，A 未完成时 B 无法置 `done`；Agent 尝试直接批准计划返回 403。

**退出标准**：Agent 不能绕过审批改 `plans.status`；删除目标时任务按策略级联或置空，不留孤儿数据。

## Phase 6 Context

**目标**：长对话不爆窗口，回答仍然接得上前面的话。

**交付物**
- `internal/ctxbuild/`：分段预算、召回与检索编排、历史裁剪
- `conversation_summaries` 迁移与摘要任务
- token 估算器 + 与真实用量的偏差校准
- 每次 run 记录上下文构成（各段 token 数），便于调参

**提交拆分**
1. `feat: 上下文分段预算与裁剪`
2. `feat: 对话摘要生成与落库`
3. `feat: 摘要注入与引用保序`
4. `feat: 上下文构成记录与调试接口`
5. `test: 超长对话、预算紧张与摘要丢失约束的回归`

**验证**：连续 30 轮对话后，`prompt_tokens` 稳定在预算内不单调增长；摘要保留"不要用 X"这类否定约束；关掉摘要任务时降级为纯截断，并有日志明示。

**退出标准**：任何情况下上下文不超预算；摘要失败不阻塞对话；用户能看到"本次回答用了哪些知识/记忆"。

## Phase 7 Insights

**目标**：数据攒起来之后，给出"我最近在干什么、学得怎么样"的判断。

**交付物**
- `internal/insight/`：统计聚合 + 模型总结
- `insights` 迁移、定时 job
- Dashboard 洞察卡片
- 学习进度：目标下任务完成曲线 + 知识覆盖度

**提交拆分**
1. `feat: 新增洞察模型与周期统计聚合`
2. `feat: 周期总结生成任务`
3. `feat: 学习进度与知识覆盖度分析`
4. `feat: 洞察页面与 Dashboard 卡片`
5. `test: 空数据、跨周期与统计口径`

**验证**：无数据时生成"暂无足够数据"而不是编造；同一周期重复生成只保留一条（唯一索引）；统计口径与 `tasks` / `documents` 手动核对一致。

**退出标准**：洞察内容只引用真实统计数据；生成失败不影响其他功能。

## Phase 8 产品完善

**目标**：可长期运行：有测试、有监控、能一键起、文档对得上代码。

**提交拆分**
1. `feat: 服务容器化与一键启动`
2. `feat: 结构化日志补充关键链路字段`
3. `feat: 队列积压与失败率指标`
4. `feat: 数据导出与备份脚本说明`
5. `test: 端到端链路集成测试`
6. `docs: 完善使用手册与接口文档`
7. `chore: 依赖升级与代码整理`

**验证**：`docker compose up -d` 一条命令起全栈；重启后未完成任务能被 worker 接管；日志里能用 `request_id` 串起一次请求；README 的每条命令都真实可执行。

## 依赖与顺序约束

- **P1 必须先把迁移与配置打磨好**：后面每个阶段都要加表，迁移机制不稳会一路还债。
- **P2 先于 P3**：记忆提炼要用到知识检索（判断某条信息是否已有文档支撑）。
- **P3 先于 P4**：Agent 的记忆工具依赖召回能力，否则只能做无记忆的问答。
- **P4 先于 P5**：行动模块的写操作要靠 Agent 的审批与审计机制兜底。
- **P6 在 P4 之后**：没有真实长对话样本，上下文预算调不出来。
- **P7 最后**：没有知识与任务数据，洞察只能编。
- **Redis 不在任何阶段的必选路径上**：单用户本地场景先用进程内缓存；只有当 SSE 广播或限流成为真实瓶颈时才引入，并单开一次提交。

## 每个阶段的固定动作

1. 阶段开始：只读 `AGENTS.md`、本文件、`docs/design/overview.md`，确认边界后动手。
2. 阶段中：设计变更先改 `docs/design/overview.md`，再改代码，避免文档失实。
3. 阶段收尾：跑三门禁（build / vet / test -race）+ 该阶段验证；更新 `AGENTS.md` 的进度行与本文件的完成标记。
4. 遗留问题记入 `AGENTS.md` 已知问题表并标注处理时机，不靠记忆留存。

## 变更管理

- 任何"提前实现后续阶段能力"的想法，先写进本文件的待办区，不直接进代码。
- 出现无法当场解决的问题，记录下来并继续推进其他部分，不允许卡住整个阶段。
- 阶段目标调整必须成对修改：本文件 + `docs/design/overview.md`。
