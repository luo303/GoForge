-- 0004：jobs 表兼任 outbox。
--
-- published_at 记录"这条任务最后一次被投递到队列的时间"，含义有三层：
--   1. NULL         = 还没发出去（事务提交了但投递失败，或进程刚崩）
--   2. 非 NULL      = 发过一次，何时发的
--   3. 长时间未更新 = 发了但没人领，需要重投
--
-- 不给默认值：默认 CURRENT_TIMESTAMP 会让"未投递"无法表达，
-- 补投也就无从判断。
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS published_at timestamptz;

-- 补投扫描只关心"排队中且没发过"的行。用部分索引而不是全表索引：
-- 已终态的任务占绝大多数，把它们排除在外，索引才能一直很小。
CREATE INDEX IF NOT EXISTS jobs_undelivered_idx
    ON jobs (run_after)
    WHERE status = 'queued' AND published_at IS NULL;

-- 另一种情况：发出去了但迟迟没人领（消息丢了、消费者那时没起）。
-- 这条索引让"按投递时间捞超时的"不必扫全表。
CREATE INDEX IF NOT EXISTS jobs_published_idx
    ON jobs (published_at)
    WHERE status = 'queued';
