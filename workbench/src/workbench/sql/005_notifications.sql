-- What each Sortie's notifications sent the workbench, through the webhook
-- backend (workbench.notifications): one row per notification, kept after
-- it is seen. payload is the body as Sortie posted it.
CREATE TABLE notifications (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  host             TEXT NOT NULL,
  notification_id  TEXT NOT NULL,
  identifier       TEXT NOT NULL DEFAULT '',
  event_type       TEXT NOT NULL DEFAULT '',
  payload          TEXT NOT NULL,
  received_at      REAL NOT NULL,
  seen_at          REAL,
  seen_by          TEXT NOT NULL DEFAULT '',
  UNIQUE (host, notification_id)
);

CREATE INDEX notifications_identifier ON notifications (identifier);
