-- Relay passwords move out of the database (2026-11, ara mail admin).
--
-- password_ref is the OpenBao KV path (relative to the mount) that holds the
-- relay's SMTP password, e.g. 'mail/relay/<relay id>'. When it is set the
-- password column is '' — the secret lives only in OpenBao. When maild runs
-- without MAIL_BAO_* the column keeps working as before (dev/tests), and a
-- plaintext row is moved to OpenBao and blanked at the next start that has it.
ALTER TABLE relay ADD COLUMN IF NOT EXISTS password_ref TEXT NOT NULL DEFAULT '';
