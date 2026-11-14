DROP INDEX IF EXISTS jobs_published_idx;
DROP INDEX IF EXISTS jobs_undelivered_idx;
ALTER TABLE jobs DROP COLUMN IF EXISTS published_at;
