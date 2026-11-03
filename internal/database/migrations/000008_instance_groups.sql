-- Persist instance groups so group management survives restarts.
CREATE TABLE IF NOT EXISTS instance_groups (
 name TEXT PRIMARY KEY,
 is_default INTEGER NOT NULL DEFAULT 0 CHECK(is_default IN (0,1)),
 created_at_ms INTEGER NOT NULL,
 updated_at_ms INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_instance_groups_default ON instance_groups(is_default) WHERE is_default = 1;
