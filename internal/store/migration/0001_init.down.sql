-- 0001 down: drop everything (reverse dependency order).
DROP TABLE IF EXISTS greylist;
DROP TABLE IF EXISTS filter_rule;
DROP TABLE IF EXISTS setting;
ALTER TABLE domain DROP COLUMN IF EXISTS relay_id;
DROP TABLE IF EXISTS relay;
DROP TABLE IF EXISTS outbound_queue;
DROP TABLE IF EXISTS message_flag;
DROP TABLE IF EXISTS message;
DROP TABLE IF EXISTS mailbox;
DROP TABLE IF EXISTS message_blob;
DROP TABLE IF EXISTS app_password;
DROP TABLE IF EXISTS address;
DROP TABLE IF EXISTS account;
DROP TABLE IF EXISTS domain;
