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
  storage: { dir: string }
  llm: ModelSettings
  embedding: ModelSettings
  agent: { max_steps: number; token_budget: number; run_timeout_seconds: number }
}
