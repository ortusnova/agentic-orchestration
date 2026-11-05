-- A story's notifications are cleared when the story leaves the state that
-- raised them (workbench.notifications): its pull request merged, its park
-- ended, it was cancelled, or its question closed. A cleared one is seen by
-- the workbench, if no person saw it first, and is no longer shown.
ALTER TABLE notifications ADD COLUMN cleared_at REAL;
ALTER TABLE notifications ADD COLUMN cleared_reason TEXT NOT NULL DEFAULT '';

-- The state each story was last seen in, by tracker identifier: merged,
-- cancelled, parked, or '' for none of them. Its change clears the
-- story's notifications, across restarts.
CREATE TABLE notification_stories (
  identifier   TEXT PRIMARY KEY,
  condition    TEXT NOT NULL,
  observed_at  REAL NOT NULL
);
