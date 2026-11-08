# Noema 产品与领域设计

## 1. 定位

Noema 是一个**在线个人知识库**：用户上传文档，系统经 Agent 流水线加工为结构化笔记，支持全文检索、标签筛选与 Markdown 导出。

一句话价值：**把一堆原始文档，变成可检索、可追溯、成本可核算的知识库。**

加工不是一次性黑盒调用，而是**可观察、可取消、可重试、可计费**的长任务。这是系统的主要工程复杂度来源。

## 2. 角色与使用方式

| 角色 | 使用方式 |
|---|---|
| 文档拥有者（当前为单用户） | 上传文档、查看加工进度、取消任务、检索笔记、导出 |
| 脚本/第三方调用方 | 通过 REST API 提交任务、查询状态 |
| 运维 | 通过探针、日志、指标观察队列积压与失败率 |

## 3. 核心流程

```text
上传文档
  → 计算内容 hash，命中已有文档则直接复用（幂等）
  → 落盘原始文件，创建 documents 记录
  → 创建 run（queued），同一事务内写 outbox 事件
  → worker 抢占 run（条件更新 + 租约）
  → 依次执行步骤：chunk → extract → summarize → tag
  → 每步完成：写 steps + usage_ledger，更新 run 进度（同一事务）
  → 全部完成：写入 notes，documents 置 ready，run 置 succeeded
  → 前端经进度查询 / SSE 看到状态变化
```

失败路径：

- 步骤失败 → 按策略重试，超过次数 run 置 failed，保留已记账用量
- 用户取消 → run 置 cancelling，正在执行的模型调用因 ctx 取消中断，最终 cancelled
- worker 崩溃 → 租约过期，其他 worker 接管；已完成步骤凭幂等键跳过，不重复计费
- 预算耗尽 → run 置 failed（`BUDGET_EXCEEDED`），不再发起新的模型调用

## 4. 领域模型

### documents（原始文档）

| 字段 | 类型 | 说明 |
|---|---|---|
| id | bigserial | 主键 |
| name | text | 原始文件名 |
| mime | text | 一期仅 `text/markdown`、`text/plain` |
| size_bytes | bigint | 文件大小，上传前做上限校验 |
| content_hash | char(64) | SHA-256，**唯一索引**，重复上传的幂等依据 |
| storage_path | text | 原始文件落盘路径 |
| status | text | `uploaded` / `processing` / `ready` / `failed` |
| created_at / updated_at | timestamptz | UTC |

### runs（一次加工任务）

| 字段 | 类型 | 说明 |
|---|---|---|
| id | bigserial | 主键 |
| document_id | bigint FK | 所属文档 |
| status | text | 见下方状态机 |
| current_step | int | 已完成的步骤数，供进度展示 |
| max_steps | int | 步骤上限，防止无限循环 |
| budget_cents | bigint | 本次任务预算（整数分） |
| used_cents | bigint | 已消耗成本 |
| total_tokens | bigint | 累计 token |
| attempt | int | 第几次执行（重试递增） |
| lease_owner | text | 持有租约的 worker 标识 |
| lease_expires_at | timestamptz | 租约过期时间，崩溃回收依据 |
| error_code / error_message | text | 失败原因（对外只给错误码） |
| started_at / finished_at | timestamptz | 执行区间 |
| created_at / updated_at | timestamptz | UTC |

索引：`(status, lease_expires_at)` 支撑抢占与过期回收；`(document_id, created_at)` 支撑文档历史。

### steps（步骤执行记录）

| 字段 | 类型 | 说明 |
|---|---|---|
| id | bigserial | 主键 |
| run_id | bigint FK | 所属任务 |
| seq | int | 步骤序号 |
| name | text | `chunk` / `extract` / `summarize` / `tag` |
| status | text | `pending` / `running` / `succeeded` / `failed` / `skipped` |
| attempt | int | 第几次尝试 |
| output_text | text | 步骤产出（中间结果可追溯） |
| tokens | bigint | 本步 token 消耗 |
| cost_cents | bigint | 本步成本 |
| error_message | text | 失败详情 |
| started_at / finished_at | timestamptz | 执行区间 |

**唯一约束 `(run_id, seq, attempt)`**：这是消费幂等的落点，重复投递不会重复记账。

### notes（加工产物）

| 字段 | 类型 | 说明 |
|---|---|---|
| id | bigserial | 主键 |
| document_id | bigint FK | **唯一**，一篇文档对应当前一份笔记 |
| run_id | bigint FK | 由哪次 run 生成，便于追溯 |
| title | text | 标题 |
| summary | text | 摘要 |
| key_points | jsonb | 要点数组 |
| content | text | 正文（Markdown） |
| tags | text[] | 标签 |
| created_at / updated_at | timestamptz | UTC |

检索：`tsvector` 生成列（title 权重 A、summary 权重 B、content 权重 C）+ GIN 索引；标签用 `text[]` 的 GIN 索引。

### usage_ledger（用量流水）

只插不改：`id / run_id / step_id / tokens / cost_cents / created_at`。对账与成本展示的真相来源，`runs.used_cents` 只是它的冗余汇总。

### outbox_events（M7 引入）

`id / aggregate_type / aggregate_id / event_type / payload(jsonb) / status / attempts / available_at / published_at / created_at`。业务写入与事件记录同事务，由 relay 投递到 MQ。

## 5. run 状态机

```text
queued ──抢占成功──> running ──全部步骤完成──> succeeded
  │                   │
  │                   ├─步骤失败且可重试─> queued（attempt+1）
  │                   ├─不可重试/预算耗尽─> failed
  │                   └─收到取消─> cancelling ──执行中断落库─> cancelled
  └─排队时取消──────────────────────────────> cancelled
```

约束：

- 只有 `queued` 且未持有效租约的 run 才可被抢占（`UPDATE ... WHERE status='queued' AND (lease_expires_at IS NULL OR lease_expires_at < now())`）。
- `succeeded` / `failed` / `cancelled` 为终态，任何转移请求返回 409。
- 所有状态转移都走条件更新，禁止"先查状态再 UPDATE"。

## 6. API 清单（/api/v1）

| 方法 | 路径 | 说明 | 里程碑 |
|---|---|---|---|
| POST | `/documents` | multipart 上传，返回 document 与 run_id（202） | M2 |
| GET | `/documents` | 分页列表，支持 status 筛选 | M2 |
| GET | `/documents/:id` | 详情，含最新 run 与 note 摘要 | M2 |
| DELETE | `/documents/:id` | 删除文档及其笔记 | M5 |
| POST | `/documents/:id/runs` | 重新加工，生成新 run | M3 |
| GET | `/runs/:id` | 状态、进度、已耗成本 | M3 |
| POST | `/runs/:id/cancel` | 取消任务 | M3 |
| GET | `/runs/:id/steps` | 步骤明细与逐步成本 | M3 |
| GET | `/runs/:id/events` | SSE 进度推送 | M7 |
| GET | `/notes` | 笔记列表，支持 `q` 全文、`tag`、分页 | M5 |
| GET | `/notes/:id` | 笔记详情 | M5 |
| GET | `/notes/:id/export` | 导出 Markdown | M5 |
| GET | `/healthz` `/readyz` | 存活 / 就绪探针 | 已有 |

错误响应统一 `httpx.Envelope`，异步受理返回 202，状态冲突返回 409。

## 7. 技术组件的业务依据

| 组件 | 解决的业务问题 | 没有它会怎样 |
|---|---|---|
| PostgreSQL | 文档、任务、笔记、流水的持久化与全文检索 | —— |
| GORM 链式 API | 事务与条件更新是核心，链式 API 覆盖最完整 | —— |
| Redis 租约 | worker 崩溃后任务可被接管 | 任务永久卡在 running |
| Redis 令牌桶 | 限制对模型的并发请求，避免触发供应商限流 | 批量上传直接 429 |
| Redis 缓存 | 进度查询与热点笔记 | 前端轮询打穿数据库 |
| RabbitMQ | 任务分发、失败重试、死信 | 长任务与 HTTP 生命周期耦合 |
| Outbox | 状态变更与事件投递不丢不重 | 进度推送丢失，前端卡死 |
| SSE | 进度实时可见 | 只能高频轮询 |

## 8. 非目标（本阶段明确不做）

- 多用户、组织、权限体系（M8 仅加最简访问令牌）
- PDF / Office 解析、网页抓取、图片 OCR
- 协同编辑、知识图谱、多 Agent 协作
- 微服务拆分与容器编排平台
- 向量检索、在线对话与知识库记忆 —— **不是不做，是排在 M8 之后**，路径见第 9 节

## 9. 后续演进：对话与知识库记忆（M9+，暂不排期）

当前产品止步于「可检索、可导出」。对话与记忆是**建在 notes 之上的第二层消费方式**，不修改 M0–M8 的任何领域模型与状态机。

同一份笔记库，两种用法：

- **全文检索**：用户给关键词，系统返回笔记列表，**用户自己读、自己拼答案**。系统是书架。
- **在线对话**：用户提问，系统召回相关笔记片段并组织成答案，**标注来源笔记**；跨会话保留稳定事实。系统是助教。

### 为什么不与 M0–M8 并行

1. 对话是第二条完整链路（切分、embedding、向量存储、召回与重排、上下文预算、会话状态、引用溯源、流式输出、对话自身的记账），工程量不低于 M3。
2. M1–M7 的复杂度已全部压在「长任务正确性」上，再叠一条「检索质量」链路，故障归因会失焦。
3. **笔记质量是召回准确率的天花板**，M5 之前没有可验证的语料，无从评价对话效果。

注意：全文检索（`tsvector` 词匹配）与向量召回（语义匹配）机制不同，M5 完成**不会自动获得**对话能力。真正共享的是 `notes` 这个结构化产物和笔记质量。

### 演进路径

| 步骤 | 内容 | 说明 |
|---|---|---|
| 1 | `notes` 增加 chunk 级向量列 | pgvector 扩展，**复用现有 PostgreSQL**，不引入独立向量数据库 |
| 2 | `conversations` / `messages` 表 | 只做上下文窗口裁剪，**不当记忆用** |
| 3 | 检索增强问答接口 | 向量召回与全文检索混合，返回答案 + 引用来源 |
| 4 | 记忆提炼 | 由模型显式提炼稳定事实写入 `memories` 表，带版本与冲突消解 |

### 一个必须先想清的分野

| | 会话记忆 | 知识库记忆 |
|---|---|---|
| 时效 | 当前这轮对话，短期 | 跨会话，长期 |
| 载体 | 消息表 + 上下文窗口裁剪 | 显式提炼的结构化事实 |
| 失效 | 会话结束即弃 | 需要版本与冲突消解 |

**禁止把聊天记录整段向量化当长期记忆**：噪声会污染召回，且事后难以定位清理。记忆必须经过显式提炼才能入库。
