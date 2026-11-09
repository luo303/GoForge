/**
 * 后端接口的类型镜像。
 *
 * 字段名保持 snake_case：和后端 JSON 完全一致，不做 camelCase 转换。
 * 转换层会引入"哪层用哪种命名"的持续判断成本，而这里的收益只是好看。
 *
 * 这些类型必须与后端 handler 的响应结构同步修改——后端加字段时，
 * 前端这里是唯一需要动的地方（页面通过类型提示自然发现变化）。
 */

export type DependencyState = 'up' | 'down' | 'missing' | 'unknown'

export type DependencyStatus = {
  status: DependencyState
  detail?: string
}

export type MigrationState = 'ok' | 'behind' | 'dirty' | 'not_initialized' | 'unknown'

export type MigrationStatus = {
  state: MigrationState
  current_version: number
  latest_version: number
  dirty: boolean
  detail?: string
}

/** GET /system/status：诊断用，永远 200，健康与否看字段 */
export type SystemStatus = {
  version: string
  postgres: DependencyStatus
  pgvector: DependencyStatus
  migration: MigrationStatus
}

export type ModelSettings = {
  api_base: string
  model: string
  /** 空串表示未配置；只含前四后四位，永远不是完整密钥 */
  api_key_masked: string
  api_key_configured: boolean
}

/** GET /settings：只读配置视图，密码字段不出现 */
export type SettingsView = {
  app: { env: string; http_addr: string }
  log: { level: string; format: string }
  db: { host: string; port: number; name: string; sslmode: string; user: string }
  redis: { addr: string; db: number; pool_size: number; password_configured: boolean }
  /** 原文只存 MinIO，所以这里没有 driver/dir：位置信息就是端点与 bucket */
  storage: {
    endpoint: string
    bucket: string
    max_upload_bytes: number
  }
  llm: ModelSettings
  embedding: ModelSettings
  agent: { max_steps: number; token_budget: number; run_timeout_seconds: number }
}

/** 文档状态。Phase 4 同步导入，成功即 ready。 */
export type DocumentStatus = 'pending' | 'parsing' | 'indexing' | 'ready' | 'failed' | 'cancelling'

export type DocumentSourceType = 'file' | 'note' | 'web'

/** GET /documents 与 /documents/:id 共用的文档视图 */
export type DocumentView = {
  id: number
  title: string
  source_type: DocumentSourceType
  mime: string
  size_bytes: number
  content_hash: string
  /** 未分类时是空串（后端把 NULL 收敛成空串，避免前端到处判 null） */
  collection: string
  /** 永远是数组，不是 null——后端已统一成 [] */
  tags: string[]
  status: DocumentStatus
  chunk_count: number
  created_at: string
  updated_at: string
}

/**
 * 导入接口的响应：多一个 existed。
 *
 * 后端用它区分"本次新建（201）"与"命中既有文档（200）"；
 * 前端据此提示用户"没有重复入库"，而不是一声不响地多出一条看起来一样的记录。
 */
export type ImportedDocumentView = DocumentView & {
  existed: boolean
}

/** 列表筛选条件。空串表示不过滤，省掉调用方处理 undefined 的分支。 */
export type DocumentFilters = {
  collection: string
  tag: string
  status: DocumentStatus | ''
}

export type DocumentQuery = DocumentFilters & {
  limit: number
  offset: number
}

/** PATCH /documents/:id 的请求体，只传要改的字段 */
export type DocumentPatch = {
  title?: string
  collection?: string
  tags?: string[]
}

/**
 * 分块。start_offset / end_offset 是 **Unicode 码点**偏移，不是字节、也不是
 * UTF-16 下标——前端要用 Array.from(text) 切片才能对齐，详见页面里的 highlight 说明。
 */
export type ChunkView = {
  id: number
  seq: number
  content: string
  heading?: string
  start_offset: number
  end_offset: number
  token_count: number
}

export type DocumentList = {
  items: DocumentView[]
  total: number
  limit: number
  offset: number
}

export type ChunkList = {
  items: ChunkView[]
  total: number
  limit: number
  offset: number
}
