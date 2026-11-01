-- Pull requests the workbench saw merged on GitHub, by owner/repo#number.
-- A merge is final, so a pull request found here is never looked up again.
CREATE TABLE merged_pulls (
  ref           TEXT PRIMARY KEY,
  url           TEXT NOT NULL,
  merge_commit  TEXT NOT NULL,
  merged_at     TEXT NOT NULL DEFAULT '',
  seen_at       INTEGER NOT NULL
);
