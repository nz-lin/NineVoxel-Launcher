-- Persist the operation group so task aggregation survives restarts.
ALTER TABLE download_tasks ADD COLUMN operation_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_download_tasks_operation ON download_tasks (operation_id);
