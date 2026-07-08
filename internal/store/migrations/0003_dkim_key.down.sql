-- 0003 rollback
ALTER TABLE domain
    DROP COLUMN IF EXISTS dkim_selector,
    DROP COLUMN IF EXISTS dkim_private_key;
