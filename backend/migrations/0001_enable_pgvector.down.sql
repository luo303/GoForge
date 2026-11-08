-- 0001 回滚：移除 pgvector 扩展。
-- 只有没有任何表使用 vector 类型时才能执行成功；
-- 若已有业务表依赖它，回滚会先失败并提示，这是预期行为。
DROP EXTENSION IF EXISTS vector;
