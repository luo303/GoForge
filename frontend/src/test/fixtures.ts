import type { ChunkView, DocumentList, DocumentView, SettingsView, SystemStatus } from '@/api/types'

/** 后端成功信封。字段与 httpx.Envelope 保持一致。 */
export function envelope<T>(data: T, requestId = 'test-request-id') {
  return { code: 'OK', message: 'success', data, request_id: requestId }
}

/** 构造一个 JSON 响应；非 2xx 时用 status 指定。 */
export function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

export function systemStatusFixture(overrides: Partial<SystemStatus> = {}): SystemStatus {
  return {
    version: 'test-1',
    postgres: { status: 'up' },
    pgvector: { status: 'up' },
    migration: { state: 'ok', current_version: 1, latest_version: 1, dirty: false },
    ...overrides,
  }
}

export function settingsFixture(overrides: Partial<SettingsView> = {}): SettingsView {
  return {
    app: { env: 'development', http_addr: ':8080' },
    log: { level: 'debug', format: 'console' },
    db: { host: '127.0.0.1', port: 5432, name: 'mindos', sslmode: 'disable', user: 'mindos' },
    redis: { addr: '127.0.0.1:6379', db: 0, pool_size: 10, password_configured: false },
    storage: {
      driver: 'minio',
      dir: './storage',
      endpoint: 'localhost:9000',
      bucket: 'mindos',
      max_upload_bytes: 10485760,
    },
    llm: {
      api_base: 'https://api.example.com/v1',
      model: 'gpt-test',
      api_key_masked: 'sk-1****wxyz',
      api_key_configured: true,
    },
    embedding: { api_base: '', model: '', api_key_masked: '', api_key_configured: false },
    agent: { max_steps: 12, token_budget: 200000, run_timeout_seconds: 300 },
    ...overrides,
  }
}

export function documentFixture(overrides: Partial<DocumentView> = {}): DocumentView {
  return {
    id: 1,
    title: '集成测试笔记',
    source_type: 'note',
    mime: 'text/markdown',
    size_bytes: 42,
    content_hash: 'a'.repeat(64),
    collection: '',
    tags: [],
    status: 'ready',
    chunk_count: 2,
    created_at: '2026-11-09T07:30:00Z',
    updated_at: '2026-11-09T07:30:00Z',
    ...overrides,
  }
}

export function chunkFixture(overrides: Partial<ChunkView> = {}): ChunkView {
  return {
    id: 1,
    seq: 0,
    content: '# 标题\n\n第一段正文。',
    heading: '标题',
    start_offset: 0,
    end_offset: 12,
    token_count: 12,
    ...overrides,
  }
}

export function documentListFixture(docs: DocumentView[]): DocumentList {
  return { items: docs, total: docs.length, limit: 20, offset: 0 }
}
