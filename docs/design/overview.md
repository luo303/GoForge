# MindOS 产品与领域设计

## 1. 定位与边界

MindOS 是一个**单用户、本地优先的 AI 个人知识与行动工作空间**。

它把三类数据放进同一个闭环：

| 数据 | 进入方式 | 用途 |
|---|---|---|
| 知识 | 文档 / 网页 / 笔记导入 | 被检索、被引用、被问答 |
| 记忆 | 从对话与活动中提炼 | 被召回、被修订、塑造回答风格与判断 |
| 行动 | 用户提出目标，Agent 拆解 | 被执行、被追踪、被确认 |

一句话价值：**让知识拥有记忆，让记忆驱动行动。**

三条设计红线，贯穿全文：

1. **可追溯**：任何一句回答都要能落到具体文档片段；任何一条记忆都要能落到具体来源；任何一次工具调用都要能落到审计记录。
2. **可修订**：记忆会错，也一定会过时。系统必须支持改、删、合并、作废，而不是只增不改。
3. **可控**：Agent 的花费、步数、可执行动作都有硬上限；写操作必须经人确认。

## 2. 单用户假设与演进路径

首期**不做注册登录、不做租户隔离**。理由不是省事，而是这个阶段的所有复杂度都该花在知识与 Agent 上：

- 单用户场景下，"是谁"这个问题没有信息量，认证只会给每个接口增加一层无意义的中间件。
- 本地部署没有信任边界：能访问 `localhost:8080` 的人就是使用者。
- 提前加 `owner_id` 会让所有查询、所有索引、所有测试都背上一个恒为 1 的条件。

**未来要多用户时怎么改**（写在这里是为了证明当前选择不是死路）：

1. 新增 `users` 表与登录；业务表统一加 `owner_id bigint not null default 1`。
2. 把 repository 的 `Where` 收口到一处构造器，加归属条件只改这一处。
3. 唯一索引从 `content_hash` 改为 `(owner_id, content_hash)`；`memories` 的部分唯一索引同样加前缀。
4. 最后再评估是否需要 RLS（行级安全）——它要求所有查询显式走事务，成本不低。

**明确不做的**：多租户计费、组织空间、协作编辑、权限矩阵。这些属于另一个产品。

## 3. 模块划分与依赖

采用**模块化单体（Modular Monolith）**：一个进程、一个数据库、一套部署，但代码按业务领域切开，模块之间只通过 service 的导出方法通信。

```text
                    ┌──────────────┐
   HTTP / SSE  ───▶ │ router       │
                    └──────┬───────┘
         ┌─────────────────┼──────────────────┐
         ▼                 ▼                  ▼
   knowledge/handler  memory/handler    chat/handler ──▶ action/handler
         │                 │                  │
         ▼                 ▼                  ▼
   knowledge/service  memory/service    chat/service
         │                 │                  │
         ▼                 ▼                  ▼
   knowledge/repo     memory/repo       chat/repo  ──▶ model
         │                 │                  │
         └────────┬────────┴──────────────────┘
                  ▼
              platform（PG / pgvector / MQ / LLM / 文件存储）

   agent（运行时）──▶ 各模块 service
   worker（消费者）──▶ 各模块 service
```

| 模块 | 职责 | 关键约束 |
|---|---|---|
| `knowledge` | 文档、解析、切分、向量化、混合检索 | 只管"内容"，不认识会话与目标 |
| `memory` | 记忆提炼、去重合并、召回打分 | 写入必须带来源；禁止整段对话入库 |
| `action` | 目标、计划、任务、依赖、进度 | 只存结构与状态，计划内容由 agent 生成 |
| `chat` | 会话、消息、SSE 推送 | 不认识 eino；只负责传输与存储 |
| `agent` | Eino 运行时封装、工具、预算、审批、checkpoint | 不认识 gin；只调 service |
| `ctxbuild` | 上下文组装、token 预算、摘要压缩 | 不发起模型调用 |
| `insight` | 周期总结与分析 | 只读知识/记忆/任务，不反向写入 |
| `job` / `worker` | 异步任务表、派发、消费 | 不写 HTTP 响应 |
| `platform` | 连接、健康检查、客户端封装 | 不出现业务词 |

**为什么按领域分包而不是按 handler/service/repository 平铺**：六个模块的生命周期差异很大（Knowledge 先上、Insights 最后上），分层平铺会让 `service` 目录长成一个 20 个文件的杂物间，模块边界靠自觉维持。按领域分包后，越界在 import 层面就能被发现。

**为什么 `chat` 和 `agent` 分开**：`chat` 是传输层（HTTP、SSE、消息落库），`agent` 是运行时（模型循环、工具、状态机）。合并后想给 Agent 换一个入口（定时任务、命令行、后台队列）就得把 gin 一起拖进去。

## 4. 核心流程

### 4.1 知识入库

下图是最终形态。实施按 Phase 4 同步导入、Phase 6 数据库任务与 worker、Phase 16 Outbox + MQ 唤醒逐步演进；jobs 始终是任务真相，后一步不推翻前一步的状态语义。

```text
上传文件 / 粘贴笔记 / 输入网址
  → 计算内容 hash，命中已有文档则直接复用（幂等）
  → 落盘原始文件（本地文件系统），创建 documents(status=pending)
  → 同一事务写 jobs(type=document_ingest) + outbox_events
  → MQ 投递唤醒 worker
  → worker 领取 job（条件更新 + locked_until）
  → 解析正文（按 mime 选 parser）→ 按标题/段落切分 → 批量 embedding
  → 写 document_chunks，documents 置 ready
  → 失败：attempts+1，未超限则 run_after 退避重投；超限置 failed 并保留错误码
```

取消：`documents` 置 `cancelling` 时，worker 通过 job 级 `context` 中断，已写入的分块回滚删除。

### 4.2 对话

下图是完整形态。Phase 13 先实现无工具对话与事件流，Phase 14 接工具与自动记忆提炼，Phase 15 接审批恢复，Phase 19 才收口完整的上下文组装。

```text
用户发消息（POST /conversations/:id/messages，SSE 响应）
  → chat/service 落库 user message，创建 agent_runs(status=running)
  → ctxbuild 组装上下文：system + 记忆召回 + 知识检索 + 最近对话 + 摘要
  → agent/runtime 启动 Eino Runner，逐事件写 agent_events 并推 SSE
  → 模型决定调用工具：读工具直接执行；写工具先发 approval_request 并挂起
  → 审批通过后 runner.Resume，从 checkpoint 继续
  → 结束：写 assistant message、usage_ledger，run 置 succeeded
  → 异步 job(memory_extract) 从本轮对话提炼记忆
```

硬约束：连接断开 → 取消 run（用户不在，继续跑只是烧钱）；超过 `max_steps` / token 预算 / 超时 → 立即终止并落 `error_code`。

### 4.3 目标到任务

```text
用户提出目标（"一个月内掌握 Go 并发"）
  → Agent 调 search_memory 拿技术栈背景，search_knowledge 拿已有资料
  → 生成 plans(status=draft) + tasks(草稿，不落库或标记 draft)
  → 展示计划，用户确认（approval）
  → 确认后落库 goals / plans / tasks，plan 置 approved
  → 用户或 Agent 更新任务状态，写 task_events
```

**关键决定：计划必须先草案、再落库。** 模型一次生成的十来个任务，八成需要人改；直接入库会污染任务列表，也无法回滚。

### 4.4 记忆写入

```text
触发源：对话结束 / 文档入库完成 / 任务完成
  → job(memory_extract) 带上下文（最近 N 轮或文档摘要）调用模型
  → 模型输出结构化候选：kind / key / title / content / importance / confidence / evidence
  → 校验 schema，逐条比对：同 (kind, key) 是否已有 active 记忆
     ├─ 无 → 新增，写 memory_versions(change=create)
     ├─ 语义相同 → 合并（更新 content/importance），写 version(change=merge)
     └─ 语义冲突 → 旧的置 superseded，新的入库并指向旧 id，写 version(change=supersede)
  → 写 memories.embedding
```

## 5. 数据模型

约定：主键 `bigserial`，时间 `timestamptz`（UTC），枚举用 `text + CHECK`，金额与 token 用整数。

### 5.1 knowledge

**documents**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | bigserial | 主键 |
| title | text | 标题 |
| source_type | text | `file` / `note` / `web` |
| source_url | text | 网页来源 |
| mime | text | `text/markdown` / `text/plain` / `application/pdf` / `text/html` |
| size_bytes | bigint | 上传前校验上限 |
| content_hash | char(64) | SHA-256，**唯一索引**，重复导入的幂等依据 |
| storage_path | text | 原始文件路径；note 类型为空 |
| content | text | note 正文 / 网页提取正文；file 类型为空 |
| collection | text | 分类（单值，够用；多维用 tags） |
| tags | text[] | 标签，GIN 索引 |
| status | text | `pending` / `parsing` / `indexing` / `ready` / `failed` / `cancelling` |
| chunk_count | int | 分块数，便于核对 |
| error_code / error_message | text | 对外只暴露错误码 |
| created_at / updated_at | timestamptz | UTC |

**document_chunks**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | bigserial | 主键 |
| document_id | bigint FK | 级联删除 |
| seq | int | 文档内序号，**(document_id, seq) 唯一**，重跑幂等的落点 |
| content | text | 分块正文 |
| heading | text | 所在章节标题，检索结果展示用 |
| start_offset / end_offset | int | 原始正文的**码点偏移**（Unicode code point，不是字节），**引用的定位依据** |
| token_count | int | 供上下文预算计算 |
| embedding | vector(1536) | HNSW 索引，`vector_cosine_ops` |
| tsv | tsvector | 生成列，GIN 索引 |
| created_at | timestamptz | UTC |

**维度为什么写死 1536**：pgvector 的列维度在迁移时确定，换 provider 就意味着重建索引与全量重算。默认对齐 `text-embedding-3-small`；要换模型，走一次"新增列 → 双写 → 回填 → 切读 → 删旧列"的迁移，不是改个配置。

### 5.2 memory

**memories**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | bigserial | 主键 |
| kind | text | `profile` / `semantic` / `episodic` / `project` |
| key | text | 归一化主题键，如 `tech_stack.backend`；可为空 |
| title | text | 一句话标题，列表展示 |
| content | text | 记忆正文（陈述句，非聊天原文） |
| importance | smallint | 1–5，影响召回权重 |
| confidence | numeric(3,2) | 0–1，模型自评 + 人工修订 |
| status | text | `active` / `archived` / `superseded` |
| source_type | text | `conversation` / `document` / `task` / `manual` |
| source_ref | jsonb | 具体来源 id，如 `{"conversation_id":3,"message_id":41}` |
| embedding | vector(1536) | 语义召回 |
| version | int | 当前版本号 |
| superseded_by_id | bigint | 被哪条新记忆取代 |
| last_recalled_at | timestamptz | 最近召回时间，用于新鲜度 |
| recall_count | int | 召回次数 |
| created_at / updated_at | timestamptz | UTC |

**部分唯一索引**：`unique (kind, key) where key is not null and status = 'active'`。
这是去重合并的落点——同一主题同时只允许一条有效记忆，写入冲突由数据库兜底，而不是靠"先查再插"。

**memory_versions**：`id / memory_id / version / title / content / change_type(create|update|merge|supersede|manual) / source_ref jsonb / created_at`，`unique (memory_id, version)`。只插不改，记忆的修订历史全在这里。

### 5.3 agent 与 chat

**conversations**：`id / title / mode(chat|plan) / status / last_message_at / created_at`
**messages**：`id / conversation_id / role(user|assistant|tool|system) / content / parts jsonb / citations jsonb / tokens / created_at`
**conversation_summaries**：`id / conversation_id / up_to_message_id / summary / tokens / created_at`——压缩后的历史，原文仍保留。

**agent_runs**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | bigserial | 主键 |
| conversation_id | bigint FK | 可为空（后台任务型 run） |
| goal_id | bigint FK | 可为空 |
| mode | text | `react` / `workflow` |
| status | text | `running` / `waiting_approval` / `succeeded` / `failed` / `cancelled` |
| step_count / max_steps | int | 硬上限，防止循环打转 |
| prompt_tokens / completion_tokens | bigint | 累计用量 |
| budget_tokens | bigint | 本次 run 的 token 上限 |
| input / output | text | 首条输入与最终回答 |
| checkpoint_id | text | 挂起时用于 Resume |
| error_code / error_message | text | 失败原因 |
| started_at / finished_at | timestamptz | 执行区间 |

**agent_events**：`id / run_id / seq / type / payload jsonb / tokens / created_at`，`unique (run_id, seq)`。
`type` 为开放枚举：`thought` / `message` / `tool_call` / `tool_result` / `approval_request` / `error` / `final`。
SSE 的推送源与事后回放源是同一张表——**先落库再推送**，断线重连用 `?since_seq=` 补齐。

**agent_checkpoints**：`key text PK / run_id / state bytea / updated_at`。
Eino 的 `CheckPointStore` 接口就是 `Set/Get(ctx, key, []byte)`，状态用 gob 序列化；不拆成列，因为它的结构随框架版本变化，拆列等于把框架内部结构焊进数据库。

**agent_approvals**：`id / run_id / event_id / tool_name / arguments jsonb / risk_level / status(pending|approved|rejected|expired) / decided_at / note / created_at`
**tool_audits**：`id / run_id / tool_name / arguments jsonb / status / duration_ms / result_digest / error_message / created_at`——参数全量、结果只存摘要（避免把整篇文档复制一遍）。

### 5.4 action

**goals**：`id / title / description / status(active|paused|achieved|archived) / priority / target_date / progress / created_by(user|agent) / completed_at`
**plans**：`id / goal_id / title / summary / status(draft|approved|active|completed|abandoned) / created_by / approved_at`
**tasks**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | bigserial | 主键 |
| goal_id / plan_id / parent_id | bigint | 三处归属都可空，允许散任务与子任务树 |
| title / description | text | 内容 |
| status | text | `todo` / `doing` / `blocked` / `done` / `cancelled` |
| priority | smallint | 1–5 |
| due_at | timestamptz | 截止时间 |
| order_index | int | 同层排序 |
| created_by | text | `user` / `agent` |
| completed_at | timestamptz | 完成时间 |

**task_dependencies**：`(task_id, depends_on_task_id)` 复合主键；写入前必须做环检测，否则"互相等待"会让进度永久卡死。
**task_events**：`id / task_id / type(create|status_change|comment|agent_update) / payload jsonb / actor(user|agent) / created_at`

### 5.5 基础设施与洞察

**jobs**：`id / type / payload jsonb / status(queued|running|succeeded|failed|cancelled) / attempts / max_attempts / run_after / locked_by / locked_until / dedupe_key / last_error / created_at / updated_at`。
索引 `(status, run_after)` 支撑领取，`(type, dedupe_key)` 支撑去重。
**DB 是任务的真相来源，MQ 只是唤醒信号**：消息丢了任务不会丢，靠定时补扫 `queued and run_after < now()` 兜底。

**outbox_events**：`id / aggregate_type / aggregate_id / event_type / payload / status / attempts / available_at / published_at / created_at`——业务写入与事件记录同事务，relay 负责投递。

**usage_ledger**：`id / scope_type(chat|document|memory|insight) / scope_id / provider / model / prompt_tokens / completion_tokens / cost_micros / created_at`。只插不改，是成本核算的唯一真相。

**app_settings**：`key text PK / value jsonb / updated_at`——LLM 与 Embedding 配置、预算上限、工具开关。单用户场景下不需要按用户分表。

**insights**：`id / period_type(daily|weekly) / period_start date / content / metrics jsonb / created_at`，`unique (period_type, period_start)`。

## 6. 检索设计（Knowledge）

### 6.1 切分

- **先按 Markdown 标题切，再按段落切，最后按 token 硬切**。标题是第一优先级的语义边界，比固定窗口靠谱得多。
- 目标块 512 token，重叠 64 token。重叠是为了避免答案正好被切断在边界上。
- 每块记录 `start_offset` / `end_offset` 与所属 `heading`——引用要精确到原文位置，而不是"某篇文档里大概说过"。
- 超长代码块不切：切碎的代码既检索不到也读不懂。

### 6.2 检索

两路召回，再用 **RRF（Reciprocal Rank Fusion）** 融合：

```text
向量路：ORDER BY embedding <=> $vec LIMIT 30
全文路：WHERE tsv @@ websearch_to_tsquery('simple', $q) ORDER BY ts_rank_cd DESC LIMIT 30
融合： score = Σ 1 / (60 + rank_i)
```

为什么必须混合：

- 纯向量对专名、版本号、错误码不敏感（"Go 1.22 的 loop variable 变更"会被泛化成"Go 语言特性"）。
- 纯全文对同义改写无能（"协程调度"搜不到"goroutine 调度"）。
- RRF 不依赖两路分数量纲，比加权求和稳；调参只需要动 `k=60` 这一个数。

排序之外还有一道**过滤**：`documents.status = 'ready'`，并可按 `collection` / `tags` 收窄。检索结果永远带 `document_id + seq + offset`，供前端高亮与引用跳转。

### 6.3 RAG 回答

- 取 top 5–8 块进上下文，按文档聚合后按相关度排序，避免同一篇文档的碎片挤满窗口。
- 回答必须带引用编号，格式 `[1]`，前端可点开查看原文片段。
- **检索不到就直说检索不到**：在 system prompt 里明确"没有依据时不要编造"，并在代码层检查引用的 `document_id` 是否真的来自本次检索结果——模型编造引用是常见失败模式。

## 7. 记忆设计（Memory）

### 7.1 四类记忆

| kind | 内容 | 例子 | 典型来源 |
|---|---|---|---|
| `profile` | 身份、背景、偏好 | 主力栈是 Node.js，正在转 Go | 对话 |
| `semantic` | 长期有效的事实与结论 | 认为 GORM 链式 API 比泛型 API 更适合本项目 | 对话 / 文档 |
| `episodic` | 带时间的历史事件 | 10 月完成过一次 PostgreSQL 迁移演练 | 对话 / 任务 |
| `project` | 项目背景与进展 | MindOS 当前处于 Phase 8，知识检索已能用 | 任务 / 文档 |

### 7.2 为什么必须"显式提炼"

把聊天记录整段向量化当记忆，是这类系统最常见的错误做法，四个后果：

1. **噪声压过信号**：一句"嗯，好的"也会被召回。
2. **改不了**：用户说"记错了，我其实不写 Java"，旧片段仍在库里，检索照样命中。
3. **冲突不可表达**：两条矛盾记忆同时存在，模型只能随机选一条。
4. **无法审计**：说不清这条记忆是从哪句话来的。

所以记忆是**结构化记录**：有主题键、有版本、有来源、有状态。聊天原文只做窗口裁剪与摘要，不进记忆表。

### 7.3 提炼

- 输入：本轮对话（或文档摘要、任务变更），带明确的 JSON schema 要求。
- 输出：候选数组，每条含 `kind / key / title / content / importance / confidence / evidence`。
- 校验：schema 不合法直接丢弃并记日志；`content` 超过长度上限截断；`evidence` 必须能在输入里找到（防幻觉）。
- 频控：同一会话 10 分钟内只提炼一次；重要性低于阈值的候选不入库。

### 7.4 合并与冲突

| 情况 | 判据 | 动作 |
|---|---|---|
| 新增 | 无同 `(kind, key)` 的 active 记忆 | 插入，`version=1` |
| 合并 | 同 key 且向量相似度 > 0.9，语义不矛盾 | 更新内容，`version+1`，`change_type=merge` |
| 取代 | 同 key 且语义冲突（如"转 Go" → "回 Node"） | 旧记录 `status=superseded` + `superseded_by_id`，新记录入库 |
| 归档 | 用户手动或过期 | `status=archived`，不再参与召回但保留历史 |

合并与取代的判定交给模型（带结构化输出），但**落库动作由代码执行**：模型只给判断，不给 SQL。

### 7.5 召回

```text
score = 0.6 × cosine_sim + 0.25 × (importance / 5) + 0.15 × recency
recency = exp(-days_since_last_recall / 30)
```

- 只召回 `status='active'`。
- 先按 `kind` 做结构化过滤（聊技术时不召回"我喜欢跑步"），再算分。
- 默认取 top 5，进上下文前按 token 预算截断。
- 召回后更新 `last_recalled_at` / `recall_count`——**频繁被用到的记忆更重要**，这是后续做记忆强化与淘汰的依据。

## 8. Agent Runtime

### 8.1 选型

用 **Eino ADK**（`github.com/cloudwego/eino/adk`）而不是手写循环：

- `ChatModelAgent` 已经实现 ReAct 循环、工具调用、Middleware 扩展点。
- `Runner` 提供事件流、`CheckPointStore`、`Interrupt/Resume`、取消（`CancelMode`），这几样手写每个都要几百行且容易错。
- `Runner` 的事件流形态（`AsyncIterator[*AgentEvent]`）天然适配 SSE。

我们做的封装集中在 `internal/agent`：把 Eino 的 Agent/Event 翻译成我们自己的领域事件（落 `agent_events`），并注入预算、审批、审计三道横切逻辑。**上层代码不直接依赖 eino 的类型**——换框架时只改这一个包。

### 8.2 执行模式

| 模式 | 触发 | 执行位置 | 断连行为 | 用途 |
|---|---|---|---|---|
| `react` | 用户发消息 | 请求 goroutine | **立即取消** | 对话、问答、临时任务 |
| `workflow` | 后台 job | worker | 不受影响 | 批量整理、周期总结、长任务 |

判断标准：**结果只有用户在等才有意义 → react；结果落库后有价值 → workflow。**

### 8.3 工具

| 工具 | 风险 | 说明 |
|---|---|---|
| `search_knowledge` | read | 混合检索，返回带引用的片段 |
| `read_document` | read | 读指定文档的正文/分块，带长度上限 |
| `search_memory` | read | 按语义 + `kind` 召回记忆 |
| `list_tasks` / `get_task` | read | 查任务与进度 |
| `save_memory` | write | 写一条记忆（走合并逻辑，不直插） |
| `create_goal` / `create_plan` / `create_task` | write | 建目标、建计划、拆任务 |
| `update_task` | write | 改状态、优先级、截止时间 |
| `summarize_period` | read | 生成周期统计原料（Phase 20） |

规则：读工具必须带条数与字符上限（否则一次检索能把上下文塞爆）；写工具必须生成 `approval_request`；任何工具的入参都要在代码里再校验一次，不信任模型给的 JSON。

### 8.4 预算

三道闸门，任一触发即终止：

1. `max_steps`（默认 12）：ReAct 循环轮数。
2. `budget_tokens`（默认按会话配置）：**检查点挂在每次模型调用的回调上**，不是每轮检查——一次思考可能连调 5 次模型，等一轮结束早就超支了。
3. 整体超时（默认 120s）：`context.WithTimeout`，同时覆盖模型与工具调用。

终止时写 `agent_runs.error_code = BUDGET_EXCEEDED | STEP_LIMIT | TIMEOUT`，并把已产生的部分结果保留在 `output` 里，避免"跑到一半什么都没留下"。

### 8.5 人工介入（HITL）

```text
模型发起写工具调用
  → agent 生成 approval_request 事件（含工具名、参数、风险说明）
  → run 状态置 waiting_approval，checkpoint 落库
  → SSE 推给前端，展示"待确认卡片"
  → 用户批准 → runner.Resume(checkpoint_id, 决策) → 继续执行
  → 用户拒绝 → 事件写 rejected，模型收到"用户拒绝"的观察结果，自行调整
  → 5 分钟未决策 → 置 expired，run 终止
```

**为什么用 checkpoint 而不是内存里挂个 channel 等**：进程重启、浏览器刷新都会让内存态消失；挂起的 run 必须能从数据库恢复。

### 8.6 事件与审计

- 每个 `AgentEvent` 落一行 `agent_events`（含 `seq`），再推 SSE。顺序不能反——先落库，断线才能补。
- 每次工具调用落一行 `tool_audits`，参数全量、结果摘要（SHA-256 + 前 200 字符）。
- `GET /runs/:id/events` 支持全量回放，前端"执行轨迹"面板直接读它。

## 9. Action（目标与行动）

### 9.1 三层结构

```text
Goal（为什么做）
  └── Plan（怎么做，一版方案）
        └── Task（具体做什么，可执行、可勾选）
              └── Task（子任务，靠 parent_id 成树）
```

`Task` 之间另有 `task_dependencies` 表达"必须先做完 A 才能做 B"，与父子关系是两回事：父子是拆分，依赖是时序。

### 9.2 状态机

```text
draft ──用户确认──> approved ──开始执行──> active ──全部完成──> completed
  │                     │                    │
  └──放弃───────────────┴────────────────────┴──> abandoned
```

任务状态：`todo → doing → done`，旁路 `blocked`（被依赖卡住）与 `cancelled`。
约束：`done` 的判定要检查依赖是否全部 `done`，否则进度会失真。

### 9.3 Agent 的边界

- Agent 可以创建 `draft` 计划与任务，**不能**直接把计划置为 `approved`。
- Agent 可以更新任务状态，但每次改动都写 `task_events` 且 `actor=agent`，用户能看出哪些不是自己动的。
- 删除操作（目标、计划）只允许用户发起，不进工具集。

## 10. Context（上下文构建）

### 10.1 组装顺序

| 顺序 | 内容 | 预算占比 | 超限策略 |
|---|---|---|---|
| 1 | system：角色、规则、引用格式、当前时间 | 10% | 固定，超了说明配置有问题 |
| 2 | 记忆：召回 top 5 的 `title + content` | 15% | 按 score 从低到高截断 |
| 3 | 知识：检索 top 5–8 分块（带序号） | 40% | 按 RRF 分数截断 |
| 4 | 历史：最近 N 轮原文 | 30% | 保留最近，丢弃最早 |
| 5 | 摘要：更早对话的 `conversation_summaries` | 5% | 只放最近一条 |

**顺序即优先级**：system 与记忆塑造"你是谁"，知识决定"答什么"，历史只影响"怎么接话"。

### 10.2 压缩

- 阈值：历史原文超过预算 30% 时触发。
- 方式：把最早的一半消息交给模型摘要，写 `conversation_summaries`，原文保留但不进上下文。
- 摘要要保留：用户明确表达的目标、约束、否定项（"不要用 X"）。这些丢了，后面的回答就会反复踩坑。
- Eino 自带 `Summarization` 中间件，Phase 19 会先评估直接复用，不够再自建。

### 10.3 Token 估算

用 `tokenizer` 精确算是理想解，但引入多语言分词依赖成本高。一期用**字符数 × 系数**估算（中英混合取 0.6），并在 `usage_ledger` 里用真实返回值做偏差校准。**预算的作用是防失控，不是精确计费**——差 10% 不影响这个目标。

## 11. Insights（个人洞察）

数据攒够之后才有意义，因此排在最后。三类产出：

1. **周期总结**：今天/本周新增了多少知识、完成了哪些任务、记忆有什么变化。
2. **学习进度**：目标下任务的完成曲线 + 相关知识库覆盖度（"Go 并发"目标下，已有哪些文档、还缺哪些主题）。
3. **关联发现**：跨文档、跨记忆的相似主题聚类（"你最近读的三篇都在讲调度器"）。

实现方式：定时 job 聚合统计 + 一次模型总结，产物写 `insights` 表。**不做实时推荐**——单用户场景下，每天一次足够。

## 12. API 清单（`/api/v1`）

单用户，无鉴权。响应统一 `httpx.Envelope`；SSE 走事件流。

**Knowledge**

| 方法 | 路径 | 说明 | 阶段 |
|---|---|---|---|
| POST | `/documents` | 文件上传（multipart）；P4 同步，P6 起 202 返回 document 与 job_id | P4 / P6 |
| POST | `/documents/note` | 新建笔记（JSON 正文） | P4 |
| POST | `/documents/web` | 按 URL 抓取导入 | P5 |
| GET | `/documents` | 列表，分页 `limit` / `offset`；`q` / `collection` / `tag` / `status` 筛选随能力接入 | P4 |
| GET | `/documents/:id` | 详情，含分块数量与状态 | P4 |
| DELETE | `/documents/:id` | 删除文档与分块 | P5 |
| POST | `/documents/:id/reindex` | 重建解析结果，P7 起同时重建向量 | P5 / P7 |
| GET | `/documents/:id/chunks` | 分块列表（调试与引用展示） | P4 |
| POST | `/search` | 混合检索，返回带引用的片段 | P8 |
| POST | `/knowledge/ask` | 基于知识库回答并返回经校验的引用 | P9 |

**Memory**

| 方法 | 路径 | 说明 | 阶段 |
|---|---|---|---|
| GET | `/memories` | 列表，支持 `kind` / `q` / `status` | P10 |
| POST | `/memories` | 手动新增 | P10 |
| PATCH | `/memories/:id` | 修改内容（写版本） | P10 |
| DELETE | `/memories/:id` | 归档（软删） | P10 |
| GET | `/memories/:id/versions` | 修订历史 | P10 |
| POST | `/memories/recall` | 调试召回打分与各分项 | P12 |

**Chat / Agent**

| 方法 | 路径 | 说明 | 阶段 |
|---|---|---|---|
| POST | `/conversations` | 新建会话 | P13 |
| GET | `/conversations` | 会话列表 | P13 |
| GET | `/conversations/:id/messages` | 消息列表 | P13 |
| POST | `/conversations/:id/messages` | 发消息，SSE 流式回答 | P13 |
| GET | `/runs/:id` | 运行状态与用量 | P13 |
| POST | `/runs/:id/cancel` | 主动取消运行 | P14 |
| GET | `/runs/:id/events` | 事件回放，支持 `since_seq` | P13 / P15 |
| POST | `/runs/:id/approvals/:approvalId` | 批准/拒绝 | P15 |

**Action**

| 方法 | 路径 | 说明 | 阶段 |
|---|---|---|---|
| GET/POST | `/goals` | 目标列表 / 新建 | P17 |
| PATCH/DELETE | `/goals/:id` | 修改 / 归档 | P17 |
| POST | `/goals/:id/plans` | 生成计划草案（Agent） | P18 |
| POST | `/plans/:id/approve` | 确认计划并落库任务 | P18 |
| GET/POST | `/tasks` | 任务列表（`today` / `status`）/ 新建 | P17 |
| PATCH | `/tasks/:id` | 改状态、优先级、截止时间 | P17 |
| GET | `/task-board` | 按目标/计划聚合的任务板 | P17 |

**Insight / 系统**

| 方法 | 路径 | 说明 | 阶段 |
|---|---|---|---|
| GET | `/dashboard` | 首页聚合：今日待办、目标进度、最近知识、重要记忆 | P9 起逐步补齐 |
| GET | `/insights` | 周期洞察列表 | P20 |
| POST | `/insights/generate` | 手动触发生成 | P20 |
| GET | `/settings` | 读取配置（密钥脱敏） | P2 |
| PUT | `/settings/llm` | LLM 配置 | P9 |
| PUT | `/settings/embedding` | Embedding 配置 | P7 |
| POST | `/settings/models/test` | 模型连通性测试 | P7 / P9 |
| GET | `/settings/tools` | 工具开关与审批策略 | P14 |
| GET | `/system/status` | 依赖健康与迁移版本，P16 增加队列状态 | P2 / P16 |
| GET | `/healthz` `/readyz` | 探针 | 已有 |

## 13. 前端信息架构

左侧导航 + 主工作区，六个页面：

| 页面 | 核心内容 | 主要交互 |
|---|---|---|
| Dashboard | 今日待办、目标进度、最近知识、重要记忆、AI 入口 | 一键跳转、快捷提问 |
| Chat | 会话列表、消息流、执行轨迹侧栏、审批卡片 | 流式输出、工具调用折叠、引用点击 |
| Knowledge | 文档列表（分类/标签/搜索）、详情、分块预览 | 拖拽上传、检索高亮、重新索引 |
| Memory | 按 kind 分组、来源与版本、编辑与归档 | 行内编辑、版本对比 |
| Tasks | 目标 → 计划 → 任务三级视图、看板 | 勾选、拖拽排序、生成计划 |
| Settings | 模型配置、工具开关、系统状态 | 连通性测试、保存即生效 |

技术约定：Vite + React Router + TanStack Query（服务端状态）+ Tailwind + shadcn/ui；**不引入 Redux**，跨页共享状态用 Context 与 Query 缓存；SSE 用原生 `EventSource` 封装成 hook，断线按 `seq` 续传。

## 14. 技术选型的业务依据

| 组件 | 解决的业务问题 | 没有它会怎样 |
|---|---|---|
| PostgreSQL | 业务数据 + 向量 + 全文检索，一个库搞定 | 三套存储、三套事务、数据一致性靠人肉 |
| pgvector | 语义召回 | 换独立向量库，运维与一致性成本翻倍 |
| 全文检索（tsvector） | 专名、错误码、版本号精确命中 | 向量检索对这些词几乎无效 |
| RabbitMQ | 长任务与请求解耦、失败重试、死信 | 文档解析卡住 HTTP 请求，重试靠重启 |
| jobs 表 | 任务真相与可观测性 | 消息丢失即任务丢失，无法对账 |
| Eino ADK | ReAct 循环、checkpoint、取消、事件流 | 手写循环，状态恢复与取消每个都是坑 |
| SSE | 流式回答与执行轨迹 | 只能轮询，用户体验差一个量级 |
| Redis（按需） | 缓存与限流 | 单用户本地场景可以先用进程内缓存，暂不引入 |
| 本地文件系统 | 原始文件存储 | 对象存储对单机部署是多余的运维负担 |

## 15. 非目标

- 多用户、组织空间、协作与权限（见第 2 节的演进路径）
- 移动端 App、浏览器插件
- 多 Agent 编排与 Agent 市场
- 知识图谱、图数据库
- 图片 OCR、音视频转写
- 付费订阅与在线支付
- 微服务拆分、Kubernetes 编排

**判断标准**：一个功能如果既不能提升"知识—记忆—行动"闭环的质量，也不能降低它的失败率，就不做。
