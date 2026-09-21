-- Permissions move from the account to groups (2026-09).
--
-- Per-account switches do not scale: every new person is a checklist. Groups
-- do. One default group holds the rules for everybody; extra groups stack on
-- top and the highest one that has an opinion wins (Discord's role model).
--
-- Grants are three-state: 'allow' | 'deny' | 'inherit' (no opinion).

CREATE TABLE IF NOT EXISTS account_group (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    -- Higher sits above and wins. The default group stays at 0.
    position   INTEGER NOT NULL DEFAULT 0,
    is_default BOOLEAN NOT NULL DEFAULT false,

    can_send             TEXT NOT NULL DEFAULT 'inherit',
    can_send_external    TEXT NOT NULL DEFAULT 'inherit',
    can_receive_external TEXT NOT NULL DEFAULT 'inherit',
    -- NULL = no opinion; the next group down decides.
    daily_send_limit     INTEGER,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT account_group_can_send_check
        CHECK (can_send IN ('allow', 'deny', 'inherit')),
    CONSTRAINT account_group_can_send_external_check
        CHECK (can_send_external IN ('allow', 'deny', 'inherit')),
    CONSTRAINT account_group_can_receive_external_check
        CHECK (can_receive_external IN ('allow', 'deny', 'inherit'))
);

-- exactly one default group
CREATE UNIQUE INDEX IF NOT EXISTS account_group_default_uniq
    ON account_group ((true)) WHERE is_default;

CREATE TABLE IF NOT EXISTS account_group_member (
    group_id   UUID NOT NULL REFERENCES account_group(id) ON DELETE CASCADE,
    account_id UUID NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, account_id)
);

CREATE INDEX IF NOT EXISTS account_group_member_account_idx
    ON account_group_member (account_id);

-- The floor everyone stands on: mail inside this server and mail arriving
-- from outside are fine; going out to the world needs a group that says so.
INSERT INTO account_group (name, position, is_default,
                           can_send, can_send_external, can_receive_external)
VALUES ('everyone', 0, true, 'allow', 'deny', 'allow')
ON CONFLICT (name) DO NOTHING;

-- A place to put the accounts that do need to reach the outside world.
INSERT INTO account_group (name, position, can_send_external)
VALUES ('external-sender', 10, 'allow')
ON CONFLICT (name) DO NOTHING;

-- The operator's own account keeps the ability to write to the outside
-- world; everybody else has to be put in the group deliberately. That is
-- the point of the change: the floor says no, and permission is something
-- an admin hands out.
INSERT INTO account_group_member (group_id, account_id)
SELECT g.id, a.id
FROM account_group g
JOIN account a ON a.kind = 'user' AND a.oidc_email = 'krisamin@krisam.in'
WHERE g.name = 'external-sender'
ON CONFLICT DO NOTHING;

ALTER TABLE account
    DROP COLUMN IF EXISTS can_send,
    DROP COLUMN IF EXISTS can_send_external,
    DROP COLUMN IF EXISTS can_receive_external,
    DROP COLUMN IF EXISTS daily_send_limit;
