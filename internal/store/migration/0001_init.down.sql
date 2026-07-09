-- 0001 rollback: 생성 역순으로 드롭 (FK 의존성 때문에)
DROP TABLE IF EXISTS message_flag;
DROP TABLE IF EXISTS message;
DROP TABLE IF EXISTS mailbox;
DROP TABLE IF EXISTS message_blob;
DROP TABLE IF EXISTS app_password;
DROP TABLE IF EXISTS account;
DROP TABLE IF EXISTS domain;
