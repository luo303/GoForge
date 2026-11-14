-- 回滚 0007：按 0003 + 0004 的原样重建 jobs 表。
--
-- 只恢复结构，不恢复数据——DROP 之后数据已经没了。
-- 保留 down 是为了让迁移链可以整体回退（CI 里会真的跑一遍 down），
-- 但这张表在业务上已经被 tasks + outbox 取代，回滚到旧代码才能用上它。

CREATE TABLE IF NOT EXISTS jobs (
    id               bigserial   PRIMARY KEY,
    kind             text        NOT NULL,
    status           text        NOT NULL DEFAULT 'queued',
    dedupe_key       text,
    payload          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    attempts         integer     NOT NULL DEFAULT 0,
    max_attempts     integer     NOT NULL DEFAULT 3,
    last_error_code  text,
    last_error       text,
    run_after        timestamptz NOT NULL DEFAULT now(),
    lease_expires_at timestamptz,
    worker_id        text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    finished_at      timestamptz,
    published_at     timestamptz,

    CONSTRAINT jobs_status_check
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelling', 'cancelled')),
    CONSTRAINT jobs_kind_check CHECK (length(kind) > 0),
    CONSTRAINT jobs_attempts_check CHECK (attempts >= 0 AND max_attempts > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_dedupe_key
    ON jobs (kind, dedupe_key)
    WHERE status IN ('queued', 'running') AND dedupe_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS jobs_claim_idx ON jobs (run_after, id) WHERE status = 'queued';

CREATE INDEX IF NOT EXISTS jobs_lease_idx ON jobs (lease_expires_at) WHERE status = 'running';

CREATE INDEX IF NOT EXISTS jobs_undelivered_idx
    ON jobs (run_after)
    WHERE status = 'queued' AND published_at IS NULL;

CREATE INDEX IF NOT EXISTS jobs_published_idx
    ON jobs (published_at)
    WHERE status = 'queued';
