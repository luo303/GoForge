-- 0002 回滚：先删子表再删父表。
-- 表上的索引与约束随表一起删除，不需要单独 DROP INDEX。
DROP TABLE IF EXISTS document_chunks;
DROP TABLE IF EXISTS documents;
