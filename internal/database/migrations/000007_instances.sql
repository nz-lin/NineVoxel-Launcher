-- Persist independent vanilla instances and their installation state.
CREATE TABLE IF NOT EXISTS instances (
 id TEXT PRIMARY KEY,
 config_json TEXT NOT NULL,
 selected INTEGER NOT NULL DEFAULT 0 CHECK(selected IN (0,1)),
 created_at_ms INTEGER NOT NULL,
 updated_at_ms INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_instances_selected ON instances(selected) WHERE selected = 1;
