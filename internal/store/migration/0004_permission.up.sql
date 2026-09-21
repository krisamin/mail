-- Mail permissions (2026-09).
--
-- Two locks on the same door: an account may send/receive externally only if
-- BOTH its own switch and its domain's switch allow it. The domain switch is
-- the operator's kill switch for a whole tenant; the account switch is the
-- per-person setting. Defaults keep every existing account exactly as it was.
ALTER TABLE account
    ADD COLUMN IF NOT EXISTS can_send             BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS can_send_external    BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS can_receive_external BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS daily_send_limit     INTEGER;  -- NULL = unlimited

ALTER TABLE domain
    ADD COLUMN IF NOT EXISTS allow_send_external    BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS allow_receive_external BOOLEAN NOT NULL DEFAULT true;

-- Daily send counter. One row per account per UTC day; the quota check and the
-- increment are the same statement so two parallel sends cannot both squeeze
-- past the limit.
CREATE TABLE IF NOT EXISTS send_counter (
    account_id UUID    NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    day        DATE    NOT NULL,
    count      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id, day)
);
