-- 0003：后台任务表（Phase 6）。
--
-- 为什么用一张表而不是 MQ：Phase 6 的目标是"任务状态可恢复"，而不是"消息吞吐"。
-- 数据库本来就参与每一次业务事务，把任务写在同一个事务里，不会出现
-- "文档建好了但任务没建"这种只靠重试去猜的中间态。MQ 在 Phase 16 只做唤醒，
-- 这张表始终是任务的真相。
--
-- 为什么把租约、尝试次数、退避时间都放在表里：
-- 这三项决定"谁在跑、还能不能跑、什么时候再跑"。放在进程内存里，
-- 进程被 kill -9 后这些信息就没了，任务会永远停在 running。

CREATE TABLE jobs (
    id               bigserial   PRIMARY KEY,
    -- 任务类型。取值由业务侧注册（document.import / document.reindex），
    -- 这里不写 CHECK：新增一种任务不该要求改数据库迁移。
    kind             text        NOT NULL,
    status           text        NOT NULL DEFAULT 'queued',
    -- 去重键：同 kind 下"同一件工作"的标识（例如 document:12:import）。
    -- 为空表示不参与去重（每次都要新建一条）。
    dedupe_key       text,
    -- 任务参数。只放 id 与选项，不放正文：原文在 MinIO / documents.content，
    -- 往这里塞一份正文等于又造了第二份真相。
    payload          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    attempts         integer     NOT NULL DEFAULT 0,
    max_attempts     integer     NOT NULL DEFAULT 3,
    -- 失败原因。code 给程序判断（是否可重试），message 给人看。
    last_error_code  text,
    last_error       text,
    -- 早于此刻不被领取：退避重试就是把它往后推。
    run_after        timestamptz NOT NULL DEFAULT now(),
    -- 租约到期时间。worker 每 lease/3 续一次；进程死了心跳就停，
    -- 租约自然过期，别的 worker 或补扫逻辑可以接管。
    lease_expires_at timestamptz,
    -- 当前持有者。排查"谁在跑"和"续约时还是不是我"都要用。
    worker_id        text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    finished_at      timestamptz,

    -- 状态机：queued → running → succeeded / failed / cancelled；
    -- 运行中收到取消请求先落 cancelling，worker 感知后转 cancelled。
    -- 用 CHECK 而不是枚举类型：加状态只改约束，不需要 ALTER TYPE。
    CONSTRAINT jobs_status_check
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelling', 'cancelled')),
    CONSTRAINT jobs_kind_check CHECK (length(kind) > 0),
    CONSTRAINT jobs_attempts_check CHECK (attempts >= 0 AND max_attempts > 0)
);

-- 去重靠这条部分唯一索引兜底，不靠应用层"先查再插"：
-- 两个并发请求同时查不到再同时插入，是这类设计最典型的漏。
-- 只约束 queued / running：已完成的历史任务不阻止再次提交同一件事。
-- dedupe_key IS NULL 的行不参与（NULL 之间互不相等）。
CREATE UNIQUE INDEX jobs_active_dedupe_key
    ON jobs (kind, dedupe_key)
    WHERE status IN ('queued', 'running') AND dedupe_key IS NOT NULL;

-- 领取条件就是 status = 'queued' AND run_after <= now()，部分索引让扫描范围
-- 只有"待办"，不会随历史任务增长而变慢。
CREATE INDEX jobs_claim_idx ON jobs (run_after, id) WHERE status = 'queued';

-- 补扫要找"running 且租约过期"，同样只索引在跑的任务。
CREATE INDEX jobs_lease_idx ON jobs (lease_expires_at) WHERE status = 'running';
