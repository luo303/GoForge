-- 0001：启用 pgvector 扩展。
-- Knowledge 阶段的 embedding 列（vector(1536)）依赖这个扩展，
-- 在还没有任何业务表的时候先把它装好，让 /system/status 能单独诊断
-- "扩展缺失"这一类问题。
CREATE EXTENSION IF NOT EXISTS vector;
