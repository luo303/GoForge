-- 0006：任务表，与 outbox 职责分离。
--
-- 分工：
--   tasks  —— 业务进度（跨重启、要给页面查、要留历史），生命周期以天计
--   outbox —— 投递通道（发出去就没价值了，定时清理），生命周期以分钟计
--
-- 旧 jobs 表被推翻的原因正是它同时扮这两个角色：状态机与待发件箱混在一起，
-- 取消、进度、重试全挤在一张表上。这次分开，并且约定：
--   tasks.status 只由消费者写；outbox.status 只由 relay 写。
CREATE TABLE IF NOT EXISTS tasks (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type          VARCHAR(64) NOT NULL,
    target_type   VARCHAR(32) NOT NULL,
    target_id     BIGINT      NOT NULL,
    status        VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempts      INT         NOT NULL DEFAULT 0,
    error_code    TEXT,
    error_message TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ,
    CONSTRAINT tasks_status_check
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled'))
);

-- 同一对象同一动作只能有一个未完成任务。
-- 这条约束替代旧 jobs 的部分唯一索引，但用业务语义表达（type + target），
-- 不再依赖字符串拼出来的 dedupe_key。
CREATE UNIQUE INDEX IF NOT EXISTS tasks_active_uniq
    ON tasks (type, target_type, target_id)
    WHERE status IN ('pending', 'running');

-- 页面按文档查历史（最近的任务）。
CREATE INDEX IF NOT EXISTS tasks_target_idx
    ON tasks (target_type, target_id, created_at DESC);

-- 补偿扫描用：pending 太久没动静的任务需要重新投递。
CREATE INDEX IF NOT EXISTS tasks_stale_idx
    ON tasks (updated_at)
    WHERE status IN ('pending', 'running');
