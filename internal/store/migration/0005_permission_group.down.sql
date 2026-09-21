ALTER TABLE account
    ADD COLUMN IF NOT EXISTS can_send             BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS can_send_external    BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS can_receive_external BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS daily_send_limit     INTEGER;

DROP TABLE IF EXISTS account_group_member;
DROP TABLE IF EXISTS account_group;
