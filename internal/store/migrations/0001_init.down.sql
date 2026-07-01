-- 0001 rollback: 생성 역순으로 드롭 (FK 의존성 때문에)
DROP TABLE IF EXISTS message_flags;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS mailboxes;
DROP TABLE IF EXISTS message_blobs;
DROP TABLE IF EXISTS app_passwords;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS domains;
