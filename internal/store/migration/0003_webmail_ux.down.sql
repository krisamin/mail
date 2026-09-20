DROP INDEX IF EXISTS message_mailbox_uid_desc_idx;
ALTER TABLE account DROP COLUMN IF EXISTS pref_locale;
ALTER TABLE account DROP COLUMN IF EXISTS pref_theme;
ALTER TABLE message DROP COLUMN IF EXISTS to_addr;
ALTER TABLE message DROP COLUMN IF EXISTS preview;
