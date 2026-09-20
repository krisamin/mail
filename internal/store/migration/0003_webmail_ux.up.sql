-- Webmail UX columns (2026-09).
--
-- preview/to_addr are caches the webmail API fills lazily on first read
-- (NULL = not computed yet), so mail that predates this migration gets them
-- the first time its mailbox is listed. The delivery path does not compute
-- them, keeping SMTP reception free of MIME parsing cost.
ALTER TABLE message ADD COLUMN IF NOT EXISTS preview text;
ALTER TABLE message ADD COLUMN IF NOT EXISTS to_addr text;

-- Per-account UI preference: colour theme and display language.
-- 'system'/'auto' mean "follow the browser".
ALTER TABLE account ADD COLUMN IF NOT EXISTS pref_theme text NOT NULL DEFAULT 'system';
ALTER TABLE account ADD COLUMN IF NOT EXISTS pref_locale text NOT NULL DEFAULT 'auto';

-- Search reads the newest rows of a mailbox first; the list path uses the
-- same order, so one composite index serves both.
CREATE INDEX IF NOT EXISTS message_mailbox_uid_desc_idx ON message (mailbox_id, uid DESC);
