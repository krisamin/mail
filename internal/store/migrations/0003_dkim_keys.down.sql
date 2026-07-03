-- 0003 rollback
ALTER TABLE domains
    DROP COLUMN IF EXISTS dkim_selector,
    DROP COLUMN IF EXISTS dkim_private_key;
